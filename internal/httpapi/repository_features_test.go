package httpapi

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestRepositoryFileCreateRenameAndDelete(t *testing.T) {
	service, credentials, head := repositoryAPIFixture(t)
	ctx := context.Background()

	tree, err := service.listRepositoryRoot(ctx, &repositoryBrowserRefInput{
		AuthInput: credentials, Repository: "engineering/api", Ref: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !tree.Body.CanEdit || tree.Body.Commit != head {
		t.Fatalf("editable tree = %#v", tree.Body)
	}

	created, err := service.createRepositoryFile(ctx, &createRepositoryFileInput{
		AuthInput: credentials, Repository: "engineering/api", Ref: "main",
		Path: "docs/guides/new.txt",
		Body: createRepositoryFileBody{
			Content: "created\n", ExpectedCommit: head,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Body.Operation != "created" ||
		created.Body.Path != "docs/guides/new.txt" ||
		created.Body.PreviousCommit != head ||
		created.Body.Commit == head ||
		created.Body.Message != "Create docs/guides/new.txt" {
		t.Fatalf("created file = %#v", created.Body)
	}
	repository, _, err := service.openBrowsableRepository(
		ctx,
		credentials,
		"engineering/api",
	)
	if err != nil {
		t.Fatal(err)
	}
	createdCommit, err := repository.CommitObject(plumbing.NewHash(created.Body.Commit))
	if err != nil {
		t.Fatal(err)
	}
	createdFile, err := createdCommit.File("docs/guides/new.txt")
	if err != nil {
		t.Fatal(err)
	}
	if contents, contentsErr := createdFile.Contents(); contentsErr != nil ||
		contents != "created\n" {
		t.Fatalf("created contents = %q, %v", contents, contentsErr)
	}

	renamed, err := service.renameRepositoryFile(ctx, &renameRepositoryFileInput{
		AuthInput: credentials, Repository: "engineering/api", Ref: "main",
		Path: "docs/guides/new.txt",
		Body: renameRepositoryFileBody{
			NewPath: "documentation/guide.txt", ExpectedCommit: created.Body.Commit,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Body.Operation != "renamed" ||
		renamed.Body.PreviousPath != "docs/guides/new.txt" ||
		renamed.Body.Path != "documentation/guide.txt" ||
		renamed.Body.Message !=
			"Rename docs/guides/new.txt to documentation/guide.txt" {
		t.Fatalf("renamed file = %#v", renamed.Body)
	}
	renamedCommit, err := repository.CommitObject(plumbing.NewHash(renamed.Body.Commit))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = renamedCommit.File("docs/guides/new.txt"); !errors.Is(err, object.ErrFileNotFound) {
		t.Fatalf("old path still exists: %v", err)
	}
	renamedFile, err := renamedCommit.File("documentation/guide.txt")
	if err != nil {
		t.Fatal(err)
	}
	if contents, contentsErr := renamedFile.Contents(); contentsErr != nil ||
		contents != "created\n" {
		t.Fatalf("renamed contents = %q, %v", contents, contentsErr)
	}

	deleted, err := service.deleteRepositoryFile(ctx, &deleteRepositoryFileInput{
		AuthInput: credentials, Repository: "engineering/api", Ref: "main",
		Path: "documentation/guide.txt",
		Body: deleteRepositoryFileBody{ExpectedCommit: renamed.Body.Commit},
	})
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Body.Operation != "deleted" ||
		deleted.Body.Path != "documentation/guide.txt" ||
		deleted.Body.Message != "Delete documentation/guide.txt" {
		t.Fatalf("deleted file = %#v", deleted.Body)
	}
	deletedCommit, err := repository.CommitObject(plumbing.NewHash(deleted.Body.Commit))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = deletedCommit.File("documentation/guide.txt"); !errors.Is(err, object.ErrFileNotFound) {
		t.Fatalf("deleted path still exists: %v", err)
	}
	deletedTree, err := deletedCommit.Tree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = deletedTree.FindEntry("documentation"); !errors.Is(err, object.ErrEntryNotFound) {
		t.Fatalf("empty parent directory still exists: %v", err)
	}
}

func TestRepositoryFileMutationsRejectConflictsAndInvalidPaths(t *testing.T) {
	service, credentials, head := repositoryAPIFixture(t)
	ctx := context.Background()

	create := func(path string, expected string) error {
		_, err := service.createRepositoryFile(ctx, &createRepositoryFileInput{
			AuthInput: credentials, Repository: "engineering/api", Ref: "main",
			Path: path,
			Body: createRepositoryFileBody{
				Content: "new\n", ExpectedCommit: expected,
			},
		})
		return err
	}
	for name, err := range map[string]error{
		"existing file": create("README.md", head),
		"invalid path":  create("../outside", head),
		"stale branch":  create("new.txt", strings.Repeat("0", 40)),
		"binary content": func() error {
			_, err := service.createRepositoryFile(ctx, &createRepositoryFileInput{
				AuthInput: credentials, Repository: "engineering/api", Ref: "main",
				Path: "binary.txt",
				Body: createRepositoryFileBody{
					Content: "binary\x00data", ExpectedCommit: head,
				},
			})
			return err
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			if err == nil {
				t.Fatal("invalid file creation succeeded")
			}
		})
	}

	for name, call := range map[string]func() error{
		"same rename path": func() error {
			_, err := service.renameRepositoryFile(ctx, &renameRepositoryFileInput{
				AuthInput: credentials, Repository: "engineering/api", Ref: "main",
				Path: "README.md",
				Body: renameRepositoryFileBody{
					NewPath: "README.md", ExpectedCommit: head,
				},
			})
			return err
		},
		"rename missing file": func() error {
			_, err := service.renameRepositoryFile(ctx, &renameRepositoryFileInput{
				AuthInput: credentials, Repository: "engineering/api", Ref: "main",
				Path: "missing.txt",
				Body: renameRepositoryFileBody{
					NewPath: "other.txt", ExpectedCommit: head,
				},
			})
			return err
		},
		"delete missing file": func() error {
			_, err := service.deleteRepositoryFile(ctx, &deleteRepositoryFileInput{
				AuthInput: credentials, Repository: "engineering/api", Ref: "main",
				Path: "missing.txt",
				Body: deleteRepositoryFileBody{ExpectedCommit: head},
			})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); err == nil {
				t.Fatal("invalid file mutation succeeded")
			}
		})
	}
}

func TestRepositoryBranchAndFileWritesWaitForOperationLock(t *testing.T) {
	for _, test := range []struct {
		name string
		call func(API, AuthInput, string) error
	}{
		{
			name: "branch",
			call: func(service API, credentials AuthInput, _ string) error {
				_, err := service.createRepositoryBranch(
					context.Background(),
					&createRepositoryBranchInput{
						AuthInput:  credentials,
						Repository: "engineering/api",
						Branch:     "operation-locked",
						From:       "main",
					},
				)
				return err
			},
		},
		{
			name: "file",
			call: func(service API, credentials AuthInput, head string) error {
				_, err := service.createRepositoryFile(
					context.Background(),
					&createRepositoryFileInput{
						AuthInput:  credentials,
						Repository: "engineering/api",
						Ref:        "main",
						Path:       "operation-locked.txt",
						Body: createRepositoryFileBody{
							Content:        "serialized\n",
							ExpectedCommit: head,
						},
					},
				)
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, credentials, head := repositoryAPIFixture(t)
			release, err := service.reviewStore().AcquireOperationLock()
			if err != nil {
				t.Fatal(err)
			}
			started := make(chan struct{})
			result := make(chan error, 1)
			go func() {
				close(started)
				result <- test.call(service, credentials, head)
			}()
			<-started
			select {
			case err = <-result:
				_ = release()
				t.Fatalf("write completed while operation lock was held: %v", err)
			case <-time.After(100 * time.Millisecond):
			}
			if err = release(); err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-result:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("write did not resume after operation lock release")
			}
		})
	}
}

func TestRepositoryArchivesContainTheSelectedTree(t *testing.T) {
	service, credentials, head := repositoryAPIFixture(t)
	repository, _, err := service.openBrowsableRepository(
		context.Background(),
		credentials,
		"engineering/api",
	)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := repository.CommitObject(plumbing.NewHash(head))
	if err != nil {
		t.Fatal(err)
	}
	tree, err := commit.Tree()
	if err != nil {
		t.Fatal(err)
	}

	var zipContents bytes.Buffer
	if err = writeRepositoryZIP(tree, commit, "api-main/", &zipContents); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(
		bytes.NewReader(zipContents.Bytes()),
		int64(zipContents.Len()),
	)
	if err != nil {
		t.Fatal(err)
	}
	zipFiles := map[string]string{}
	for _, file := range reader.File {
		contents, openErr := file.Open()
		if openErr != nil {
			t.Fatal(openErr)
		}
		value, readErr := io.ReadAll(contents)
		closeErr := contents.Close()
		if readErr != nil || closeErr != nil {
			t.Fatalf("read ZIP entry: %v, %v", readErr, closeErr)
		}
		zipFiles[file.Name] = string(value)
	}
	if zipFiles["api-main/README.md"] != "api\n" || len(zipFiles) != 1 {
		t.Fatalf("ZIP files = %#v", zipFiles)
	}

	var tarContents bytes.Buffer
	if err = writeRepositoryTarGzip(tree, commit, "api-main/", &tarContents); err != nil {
		t.Fatal(err)
	}
	compressed, err := gzip.NewReader(&tarContents)
	if err != nil {
		t.Fatal(err)
	}
	archive := tar.NewReader(compressed)
	header, err := archive.Next()
	if err != nil {
		t.Fatal(err)
	}
	contents, err := io.ReadAll(archive)
	if err != nil {
		t.Fatal(err)
	}
	if header.Name != "api-main/README.md" || string(contents) != "api\n" {
		t.Fatalf("tar entry = %#v, %q", header, contents)
	}
	if _, err = archive.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("unexpected second tar entry: %v", err)
	}
	if err = compressed.Close(); err != nil {
		t.Fatal(err)
	}

	if archiveName("api", "feature/docs") != "api-feature-docs" {
		t.Fatalf("archive name = %q", archiveName("api", "feature/docs"))
	}
	if _, err = archiveEntryName("api/", "../outside"); err == nil {
		t.Fatal("unsafe archive entry was accepted")
	}
}
