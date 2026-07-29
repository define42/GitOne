package storage

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/define42/GitOne/internal/control"
	"github.com/define42/GitOne/internal/lockmgr"
	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/review"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func assertMainDefaultWithoutMaster(t *testing.T, repository *git.Repository, mainExists bool) {
	t.Helper()
	head, err := repository.Reference(plumbing.HEAD, false)
	if err != nil {
		t.Fatalf("read HEAD: %v", err)
	}
	if head.Type() != plumbing.SymbolicReference ||
		head.Target() != plumbing.NewBranchReferenceName("main") {
		t.Fatalf("HEAD does not point to main: %s", head)
	}
	_, err = repository.Reference(plumbing.NewBranchReferenceName("master"), false)
	if !errors.Is(err, plumbing.ErrReferenceNotFound) {
		t.Fatalf("master branch unexpectedly exists: %v", err)
	}
	_, err = repository.Reference(plumbing.NewBranchReferenceName("main"), false)
	if mainExists && err != nil {
		t.Fatalf("main branch does not exist: %v", err)
	}
	if !mainExists && !errors.Is(err, plumbing.ErrReferenceNotFound) {
		t.Fatalf("empty repository unexpectedly has a main branch: %v", err)
	}
}

func TestCreateGroupAndRepository(t *testing.T) {
	root := t.TempDir()
	s := Store{Root: root}
	if e := s.CreateGroup("engineering", "alice", "Engineering projects"); e != nil {
		t.Fatal(e)
	}
	document, err := control.NewStore(root).Load(context.Background(), "engineering")
	if err != nil {
		t.Fatal(err)
	}
	if document.Version != control.CurrentVersion ||
		document.Visibility != "private" ||
		!document.LFS.Enabled {
		t.Fatalf("unexpected default group policy: %#v", document)
	}
	r := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	if e := s.CreateRepository(r, CreateRepositoryOptions{}); e != nil {
		t.Fatal(e)
	}
	controlRepository, err := git.PlainOpen(filepath.Join(root, "engineering", "control.git"))
	if err != nil {
		t.Fatal(err)
	}
	assertMainDefaultWithoutMaster(t, controlRepository, true)
	emptyRepository, err := git.PlainOpen(filepath.Join(root, "engineering", "api.git"))
	if err != nil {
		t.Fatal(err)
	}
	assertMainDefaultWithoutMaster(t, emptyRepository, false)
	description, err := s.RepositoryDescription(r)
	if err != nil {
		t.Fatal(err)
	}
	if description != "" {
		t.Fatalf("unexpected empty repository description: %q", description)
	}
	for _, p := range []string{filepath.Join(root, "engineering", "control.git"), filepath.Join(root, "engineering", "api.git"), filepath.Join(root, "engineering", "api.lfs", "objects")} {
		if _, e := os.Stat(p); e != nil {
			t.Fatalf("missing %s: %v", p, e)
		}
	}
}

func TestUpdateGroupControl(t *testing.T) {
	root := t.TempDir()
	store := Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", "Before"); err != nil {
		t.Fatal(err)
	}
	controls := control.NewStore(root)
	document, err := controls.Load(context.Background(), "engineering")
	if err != nil {
		t.Fatal(err)
	}
	document.Description = "After"
	document.Inherit = false
	document.Visibility = "internal"
	document.LFS = control.LFSPolicy{
		Enabled:             true,
		MaximumObjectBytes:  1024,
		MaximumStorageBytes: 4096,
	}
	document.Members["bob"] = control.RoleDeveloper
	document.Tokens = append(document.Tokens, control.Token{
		Name: "deploy",
		Key:  "ci",
		Hash: "sha256:test",
		Role: control.RoleDeveloper,
	})
	if err = store.UpdateGroupControl("engineering", document, "alice"); err != nil {
		t.Fatal(err)
	}
	updated, err := control.NewStore(root).Load(context.Background(), "engineering")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Description != "After" ||
		updated.Inherit ||
		updated.Members["bob"] != control.RoleDeveloper ||
		len(updated.Tokens) != 1 ||
		updated.Visibility != "internal" ||
		!updated.LFS.Enabled ||
		updated.LFS.MaximumObjectBytes != 1024 ||
		updated.LFS.MaximumStorageBytes != 4096 {
		t.Fatalf("unexpected updated control document: %#v", updated)
	}

	repository, err := git.PlainOpen(filepath.Join(root, "engineering", "control.git"))
	if err != nil {
		t.Fatal(err)
	}
	head, err := repository.Head()
	if err != nil {
		t.Fatal(err)
	}
	commit, err := repository.CommitObject(head.Hash())
	if err != nil {
		t.Fatal(err)
	}
	if commit.Message != "Update group control\n" ||
		commit.Author.Name != "alice" ||
		len(commit.ParentHashes) != 1 {
		t.Fatalf("unexpected control commit: %#v", commit)
	}
}

func TestUpdateGroupControlWaitsForOperationLock(t *testing.T) {
	root := t.TempDir()
	store := Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", "Before"); err != nil {
		t.Fatal(err)
	}
	document, err := control.NewStore(root).Load(context.Background(), "engineering")
	if err != nil {
		t.Fatal(err)
	}
	document.Description = "After"
	release, err := review.NewStore(root).AcquireOperationLock()
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		result <- store.UpdateGroupControl("engineering", document, "alice")
	}()
	select {
	case err = <-result:
		_ = release()
		t.Fatalf("control update completed while operation lock was held: %v", err)
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
		t.Fatal("control update did not resume after operation lock release")
	}
	updated, err := control.NewStore(root).Load(context.Background(), "engineering")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Description != "After" {
		t.Fatalf("control description = %q", updated.Description)
	}
}

func TestCreateRepositoryWithReadme(t *testing.T) {
	root := t.TempDir()
	store := Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	repositoryPath := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	if err := store.CreateRepository(repositoryPath, CreateRepositoryOptions{
		InitializeReadme: true,
		Author:           "alice",
	}); err != nil {
		t.Fatal(err)
	}

	repository, err := git.PlainOpen(filepath.Join(root, "engineering", "api.git"))
	if err != nil {
		t.Fatal(err)
	}
	assertMainDefaultWithoutMaster(t, repository, true)
	head, err := repository.Head()
	if err != nil {
		t.Fatal(err)
	}
	commit, err := repository.CommitObject(head.Hash())
	if err != nil {
		t.Fatal(err)
	}
	if commit.Author.Name != "alice" {
		t.Fatalf("unexpected commit author: %q", commit.Author.Name)
	}
	tree, err := commit.Tree()
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.Entries) != 1 || tree.Entries[0].Name != "README.md" {
		t.Fatalf("unexpected initial tree: %#v", tree.Entries)
	}
	readme, err := commit.File("README.md")
	if err != nil {
		t.Fatal(err)
	}
	contents, err := readme.Contents()
	if err != nil {
		t.Fatal(err)
	}
	if contents != "api\n" {
		t.Fatalf("unexpected README.md contents: %q", contents)
	}
}

func TestCreateRepositoryWithDescriptionOnly(t *testing.T) {
	root := t.TempDir()
	store := Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	repositoryPath := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	if err := store.CreateRepository(repositoryPath, CreateRepositoryOptions{
		Author:      "alice",
		Description: "Backend API",
	}); err != nil {
		t.Fatal(err)
	}

	repository, err := git.PlainOpen(filepath.Join(root, "engineering", "api.git"))
	if err != nil {
		t.Fatal(err)
	}
	head, err := repository.Head()
	if err != nil {
		t.Fatal(err)
	}
	commit, err := repository.CommitObject(head.Hash())
	if err != nil {
		t.Fatal(err)
	}
	tree, err := commit.Tree()
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.Entries) != 1 || tree.Entries[0].Name != ".gitone.yaml" {
		t.Fatalf("unexpected description-only tree: %#v", tree.Entries)
	}
	metadata, err := commit.File(".gitone.yaml")
	if err != nil {
		t.Fatal(err)
	}
	contents, err := metadata.Contents()
	if err != nil {
		t.Fatal(err)
	}
	if contents != "description: Backend API\n" {
		t.Fatalf("unexpected .gitone.yaml contents: %q", contents)
	}
	description, err := store.RepositoryDescription(repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	if description != "Backend API" {
		t.Fatalf("unexpected repository description: %q", description)
	}
}

func TestImportRepositoryCleansUpFailedRemote(t *testing.T) {
	remote := httptest.NewServer(http.NotFoundHandler())
	defer remote.Close()

	root := t.TempDir()
	store := Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	repository := repopath.Repository{Groups: []string{"engineering"}, Name: "imported"}
	err := store.ImportRepository(context.Background(), repository, ImportRepositoryOptions{
		URL: remote.URL + "/missing.git",
	})
	var remoteError *RemoteImportError
	if !errors.As(err, &remoteError) {
		t.Fatalf("failed import error = %v, want RemoteImportError", err)
	}
	for _, path := range []string{
		filepath.Join(root, "engineering", "imported.git"),
		filepath.Join(root, "engineering", "imported.lfs"),
	} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("failed import left %s: %v", path, statErr)
		}
	}
	entries, err := os.ReadDir(filepath.Join(root, "engineering"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".gitone-import-") {
			t.Fatalf("failed import left temporary data %q", entry.Name())
		}
	}
	staged, err := os.ReadDir(filepath.Join(root, ".gitone", "imports"))
	if err != nil {
		t.Fatal(err)
	}
	if len(staged) != 0 {
		t.Fatalf("failed import left %d staged repositories", len(staged))
	}
}

func TestImportRepositoryLocksOnlyFinalPublishAndRevalidates(t *testing.T) {
	sourceRoot := t.TempDir()
	sourceStore := Store{Root: sourceRoot}
	if err := sourceStore.CreateGroup("source", "alice", ""); err != nil {
		t.Fatal(err)
	}
	source := repopath.Repository{Groups: []string{"source"}, Name: "api"}
	if err := sourceStore.CreateRepository(source, CreateRepositoryOptions{
		InitializeReadme: true,
		Author:           "alice",
	}); err != nil {
		t.Fatal(err)
	}
	sourcePath, err := sourceStore.GitPath(source)
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	store := Store{Root: root}
	if err = store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	target := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	unrelated := repopath.Repository{Groups: []string{"engineering"}, Name: "web"}
	validationStarted := make(chan struct{})
	releaseValidation := make(chan struct{})
	rejected := errors.New("authorization changed")
	importDone := make(chan error, 1)
	go func() {
		importDone <- store.ImportRepositoryValidated(
			context.Background(),
			target,
			ImportRepositoryOptions{URL: sourcePath},
			func() error {
				close(validationStarted)
				<-releaseValidation
				return rejected
			},
		)
	}()
	select {
	case <-validationStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("import did not reach final validation")
	}

	unrelatedDone := make(chan error, 1)
	go func() {
		unrelatedDone <- store.CreateRepository(unrelated, CreateRepositoryOptions{})
	}()
	select {
	case err = <-unrelatedDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("unrelated repository creation was blocked by final import validation")
	}

	targetDone := make(chan error, 1)
	go func() {
		targetDone <- store.CreateRepository(target, CreateRepositoryOptions{})
	}()
	select {
	case err = <-targetDone:
		close(releaseValidation)
		t.Fatalf("target repository bypassed the import publication lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseValidation)
	if err = <-importDone; !errors.Is(err, rejected) {
		t.Fatalf("import error = %v, want validation rejection", err)
	}
	if err = <-targetDone; err != nil {
		t.Fatal(err)
	}
	staged, err := os.ReadDir(filepath.Join(root, ".gitone", "imports"))
	if err != nil {
		t.Fatal(err)
	}
	if len(staged) != 0 {
		t.Fatalf("rejected import left %d staged repositories", len(staged))
	}
}

func TestImportRepositoryArchive(t *testing.T) {
	sourceRoot := t.TempDir()
	sourceStore := Store{Root: sourceRoot}
	if err := sourceStore.CreateGroup("source", "alice", ""); err != nil {
		t.Fatal(err)
	}
	source := repopath.Repository{Groups: []string{"source"}, Name: "api"}
	if err := sourceStore.CreateRepository(source, CreateRepositoryOptions{
		InitializeReadme: true,
		Author:           "alice",
		Description:      "Archived API",
	}); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(sourceRoot, "source", "api.git")
	sourceRepository, err := git.PlainOpen(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	sourceHead, err := sourceRepository.Head()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(sourcePath, "hooks"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(sourcePath, "objects", "info"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(
		filepath.Join(sourcePath, "hooks", "post-receive"),
		[]byte("#!/bin/sh\nexit 1\n"),
		0o750,
	); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(
		filepath.Join(sourcePath, "objects", "info", "alternates"),
		[]byte("/untrusted/object/store\n"),
		0o640,
	); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name     string
		filename string
		prefix   string
		write    func(*testing.T, string, string, string)
	}{
		{
			name:     "ZIP repository at archive root",
			filename: "api.zip",
			write:    writeRepositoryZIP,
		},
		{
			name:     "compressed TAR with enclosing folder",
			filename: "api.tar.gz",
			prefix:   "api.git",
			write:    writeRepositoryTARGZ,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			archivePath := filepath.Join(t.TempDir(), test.filename)
			test.write(t, sourcePath, archivePath, test.prefix)

			targetRoot := t.TempDir()
			targetStore := Store{Root: targetRoot}
			if createErr := targetStore.CreateGroup("engineering", "alice", ""); createErr != nil {
				t.Fatal(createErr)
			}
			target := repopath.Repository{
				Groups: []string{"engineering"},
				Name:   "imported",
			}
			if importErr := targetStore.ImportRepositoryArchive(
				context.Background(),
				target,
				test.filename,
				archivePath,
			); importErr != nil {
				t.Fatal(importErr)
			}

			importedPath := filepath.Join(targetRoot, "engineering", "imported.git")
			imported, openErr := git.PlainOpen(importedPath)
			if openErr != nil {
				t.Fatal(openErr)
			}
			importedHead, headErr := imported.Head()
			if headErr != nil {
				t.Fatal(headErr)
			}
			if importedHead.Hash() != sourceHead.Hash() {
				t.Fatalf("imported HEAD = %s, want %s", importedHead.Hash(), sourceHead.Hash())
			}
			if _, worktreeErr := imported.Worktree(); !errors.Is(
				worktreeErr,
				git.ErrIsBareRepository,
			) {
				t.Fatalf("imported repository is not bare: %v", worktreeErr)
			}
			description, descriptionErr := targetStore.RepositoryDescription(target)
			if descriptionErr != nil {
				t.Fatal(descriptionErr)
			}
			if description != "Archived API" {
				t.Fatalf("imported description = %q", description)
			}
			for _, expected := range []string{
				filepath.Join(targetRoot, "engineering", "imported.lfs", "objects"),
			} {
				if _, statErr := os.Stat(expected); statErr != nil {
					t.Fatalf("missing imported repository data %s: %v", expected, statErr)
				}
			}
			for _, removed := range []string{
				filepath.Join(importedPath, "hooks"),
				filepath.Join(importedPath, "objects", "info", "alternates"),
			} {
				if _, statErr := os.Stat(removed); !os.IsNotExist(statErr) {
					t.Fatalf("unsafe imported path still exists %s: %v", removed, statErr)
				}
			}
		})
	}
}

func TestImportRepositoryArchiveRejectsUnsafeEntries(t *testing.T) {
	for _, test := range []struct {
		name     string
		filename string
		write    func(*testing.T, string)
	}{
		{
			name:     "ZIP path traversal",
			filename: "unsafe.zip",
			write: func(t *testing.T, archivePath string) {
				t.Helper()
				output, err := os.Create(archivePath)
				if err != nil {
					t.Fatal(err)
				}
				writer := zip.NewWriter(output)
				entry, err := writer.Create("../escaped")
				if err != nil {
					t.Fatal(err)
				}
				if _, err = entry.Write([]byte("escaped")); err != nil {
					t.Fatal(err)
				}
				if err = writer.Close(); err != nil {
					t.Fatal(err)
				}
				if err = output.Close(); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:     "TAR symbolic link",
			filename: "unsafe.tar",
			write: func(t *testing.T, archivePath string) {
				t.Helper()
				output, err := os.Create(archivePath)
				if err != nil {
					t.Fatal(err)
				}
				writer := tar.NewWriter(output)
				if err = writer.WriteHeader(&tar.Header{
					Name:     "repository.git/HEAD",
					Typeflag: tar.TypeSymlink,
					Linkname: "/etc/passwd",
					Mode:     0o777,
				}); err != nil {
					t.Fatal(err)
				}
				if err = writer.Close(); err != nil {
					t.Fatal(err)
				}
				if err = output.Close(); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			store := Store{Root: root}
			if err := store.CreateGroup("engineering", "alice", ""); err != nil {
				t.Fatal(err)
			}
			archivePath := filepath.Join(t.TempDir(), test.filename)
			test.write(t, archivePath)
			repository := repopath.Repository{
				Groups: []string{"engineering"},
				Name:   "imported",
			}
			err := store.ImportRepositoryArchive(
				context.Background(),
				repository,
				test.filename,
				archivePath,
			)
			var archiveError *ArchiveImportError
			if !errors.As(err, &archiveError) {
				t.Fatalf("unsafe archive error = %v, want ArchiveImportError", err)
			}
			for _, unexpected := range []string{
				filepath.Join(root, "engineering", "escaped"),
				filepath.Join(root, "engineering", "imported.git"),
				filepath.Join(root, "engineering", "imported.lfs"),
			} {
				if _, statErr := os.Stat(unexpected); !os.IsNotExist(statErr) {
					t.Fatalf("unsafe import created %s: %v", unexpected, statErr)
				}
			}
			entries, readErr := os.ReadDir(filepath.Join(root, "engineering"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".gitone-import-") {
					t.Fatalf("unsafe import left temporary data %q", entry.Name())
				}
			}
		})
	}
}

func writeRepositoryZIP(
	t *testing.T,
	repositoryPath string,
	archivePath string,
	prefix string,
) {
	t.Helper()
	output, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(output)
	err = filepath.Walk(repositoryPath, func(current string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, relativeErr := filepath.Rel(repositoryPath, current)
		if relativeErr != nil {
			return relativeErr
		}
		if relative == "." {
			return nil
		}
		name := path.Join(prefix, filepath.ToSlash(relative))
		header, headerErr := zip.FileInfoHeader(info)
		if headerErr != nil {
			return headerErr
		}
		header.Name = name
		if info.IsDir() {
			header.Name += "/"
			_, headerErr = writer.CreateHeader(header)
			return headerErr
		}
		entry, createErr := writer.CreateHeader(header)
		if createErr != nil {
			return createErr
		}
		source, openErr := os.Open(current)
		if openErr != nil {
			return openErr
		}
		_, copyErr := io.Copy(entry, source)
		closeErr := source.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err = output.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeRepositoryTARGZ(
	t *testing.T,
	repositoryPath string,
	archivePath string,
	prefix string,
) {
	t.Helper()
	output, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	compressed := gzip.NewWriter(output)
	writer := tar.NewWriter(compressed)
	err = filepath.Walk(repositoryPath, func(current string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, relativeErr := filepath.Rel(repositoryPath, current)
		if relativeErr != nil {
			return relativeErr
		}
		if relative == "." {
			return nil
		}
		header, headerErr := tar.FileInfoHeader(info, "")
		if headerErr != nil {
			return headerErr
		}
		header.Name = path.Join(prefix, filepath.ToSlash(relative))
		if headerErr = writer.WriteHeader(header); headerErr != nil || info.IsDir() {
			return headerErr
		}
		source, openErr := os.Open(current)
		if openErr != nil {
			return openErr
		}
		_, copyErr := io.Copy(writer, source)
		closeErr := source.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err = compressed.Close(); err != nil {
		t.Fatal(err)
	}
	if err = output.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateRepositoryRejectsInvalidStateAndCleansUpGit(t *testing.T) {
	root := t.TempDir()
	store := Store{Root: root}
	repositoryPath := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}

	if err := store.CreateRepository(repositoryPath, CreateRepositoryOptions{}); err == nil {
		t.Fatal("repository was created without its group")
	}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRepository(
		repopath.Repository{Groups: []string{"engineering"}, Name: "control"},
		CreateRepositoryOptions{},
	); err == nil {
		t.Fatal("reserved control repository was created")
	}

	blockedLFS := filepath.Join(root, "engineering", "api.lfs")
	if err := os.WriteFile(blockedLFS, []byte("not a directory"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRepository(repositoryPath, CreateRepositoryOptions{}); err == nil {
		t.Fatal("repository was created with an unusable LFS path")
	}
	gitPath, err := store.GitPath(repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(gitPath); !os.IsNotExist(err) {
		t.Fatalf("failed repository creation left Git data behind: %v", err)
	}
	if err = os.Remove(blockedLFS); err != nil {
		t.Fatal(err)
	}

	if err = store.CreateRepository(repositoryPath, CreateRepositoryOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = store.CreateRepository(repositoryPath, CreateRepositoryOptions{}); err == nil {
		t.Fatal("duplicate repository was created")
	}
}

func TestSubgroupNeedsParent(t *testing.T) {
	s := Store{Root: t.TempDir()}
	if e := s.CreateGroup("a/b", "alice", ""); e == nil {
		t.Fatal("expected parent error")
	}
}

func TestCreateTopLevelGroupWithRelativeRoot(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Rel(workingDirectory, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := Store{Root: root}

	if err = s.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatalf("create top-level group: %v", err)
	}
	if _, err = os.Stat(filepath.Join(root, "engineering", "control.git")); err != nil {
		t.Fatalf("control repository was not created: %v", err)
	}
}

func TestListGroupsAndRepositories(t *testing.T) {
	s := Store{Root: t.TempDir()}
	if err := s.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateGroup("engineering/backend", "alice", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRepository(repopath.Repository{Groups: []string{"engineering", "backend"}, Name: "api"}, CreateRepositoryOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(s.Root, "engineering", "backend", "api.build"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(
		filepath.Join(
			s.Root,
			"engineering",
			"backend",
			"api.reviews",
			"not-a-group",
			"control.git",
		),
		0o750,
	); err != nil {
		t.Fatal(err)
	}

	groups, err := s.ListGroups()
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups, got %#v", groups)
	}
	if groups[0].Path != "engineering" || len(groups[0].Repositories) != 0 {
		t.Fatalf("unexpected top-level group: %#v", groups[0])
	}
	if groups[1].Path != "engineering/backend" ||
		len(groups[1].Repositories) != 1 ||
		groups[1].Repositories[0] != "api" {
		t.Fatalf("unexpected subgroup: %#v", groups[1])
	}
}

func TestRepositoryAndGroupDeletionPreservesDataInTrash(t *testing.T) {
	root := t.TempDir()
	store := Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	repositoryPath := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	if err := store.CreateRepository(repositoryPath, CreateRepositoryOptions{
		InitializeReadme: true,
		Author:           "alice",
	}); err != nil {
		t.Fatal(err)
	}
	lfsPath, err := store.LFSPath(repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	payloadPath := filepath.Join(lfsPath, "objects", "aa", "bb", "payload")
	if err = os.MkdirAll(filepath.Dir(payloadPath), 0o750); err != nil {
		t.Fatal(err)
	}
	payload := []byte("preserve this LFS object")
	if err = os.WriteFile(payloadPath, payload, 0o640); err != nil {
		t.Fatal(err)
	}
	buildPath, err := store.BuildPath(repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(buildPath, 0o750); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(buildPath, "build-1.log"), []byte("build output"), 0o640); err != nil {
		t.Fatal(err)
	}
	reviews := review.NewStore(root)
	mergeRequest := review.MergeRequest{
		Title:      "Ship the feature",
		Target:     "main",
		Source:     "feature",
		Author:     "alice",
		BaseCommit: strings.Repeat("1", 40),
		HeadCommit: strings.Repeat("2", 40),
	}
	if err = reviews.Create(repositoryPath, &mergeRequest); err != nil {
		t.Fatal(err)
	}

	if err = store.RenameRepository(repositoryPath, "service"); err != nil {
		t.Fatal(err)
	}
	renamedPath := repopath.Repository{Groups: []string{"engineering"}, Name: "service"}
	renamedGitPath, err := store.GitPath(renamedPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = git.PlainOpen(renamedGitPath); err != nil {
		t.Fatalf("open renamed repository: %v", err)
	}
	renamedLFSPath, err := store.LFSPath(renamedPath)
	if err != nil {
		t.Fatal(err)
	}
	if contents, readErr := os.ReadFile(filepath.Join(renamedLFSPath, "objects", "aa", "bb", "payload")); readErr != nil ||
		string(contents) != string(payload) {
		t.Fatalf("renamed LFS payload = %q, %v", contents, readErr)
	}
	renamedBuildPath, err := store.BuildPath(renamedPath)
	if err != nil {
		t.Fatal(err)
	}
	if contents, readErr := os.ReadFile(filepath.Join(renamedBuildPath, "build-1.log")); readErr != nil ||
		string(contents) != "build output" {
		t.Fatalf("renamed build log = %q, %v", contents, readErr)
	}
	renamedReviewPath, err := store.ReviewPath(renamedPath)
	if err != nil {
		t.Fatal(err)
	}
	renamedRequest, err := review.NewStore(root).Get(renamedPath, mergeRequest.ID)
	if err != nil {
		t.Fatalf("load renamed merge request: %v", err)
	}
	if renamedRequest.Repository != renamedPath.Full() ||
		renamedRequest.Title != mergeRequest.Title {
		t.Fatalf("unexpected renamed merge request: %#v", renamedRequest)
	}

	if err = store.DeleteRepository(renamedPath); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		renamedGitPath,
		renamedLFSPath,
		renamedBuildPath,
		renamedReviewPath,
	} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("deleted repository path still exists: %s: %v", path, statErr)
		}
	}

	var foundGit, foundPayload, foundBuildLog, foundReview bool
	err = filepath.Walk(filepath.Join(root, ".trash"), func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		switch info.Name() {
		case "service.git":
			if _, openErr := git.PlainOpen(path); openErr != nil {
				t.Fatalf("trashed Git repository cannot be opened: %v", openErr)
			}
			foundGit = true
		case "payload":
			contents, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			if string(contents) != string(payload) {
				t.Fatalf("trashed LFS payload = %q, want %q", contents, payload)
			}
			foundPayload = true
		case "build-1.log":
			contents, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			if string(contents) != "build output" {
				t.Fatalf("trashed build log = %q", contents)
			}
			foundBuildLog = true
		case "service.reviews":
			contents, readErr := os.ReadFile(
				filepath.Join(path, fmt.Sprintf("%d.json", mergeRequest.ID)),
			)
			if readErr != nil {
				return readErr
			}
			if !strings.Contains(
				string(contents),
				`"repository": "engineering/service"`,
			) {
				t.Fatalf("trashed review record has stale repository: %s", contents)
			}
			foundReview = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !foundGit || !foundPayload || !foundBuildLog || !foundReview {
		t.Fatalf(
			"trash is missing repository data: git=%v lfs=%v build=%v review=%v",
			foundGit,
			foundPayload,
			foundBuildLog,
			foundReview,
		)
	}

	if err = store.DeleteGroup("engineering"); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(root, "engineering")); !os.IsNotExist(err) {
		t.Fatalf("deleted group still exists: %v", err)
	}
}

func TestRenameRepositoryRollsBackGitWhenLFSMoveFails(t *testing.T) {
	root := t.TempDir()
	store := Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	repositoryPath := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	if err := store.CreateRepository(repositoryPath, CreateRepositoryOptions{}); err != nil {
		t.Fatal(err)
	}
	originalGit, err := store.GitPath(repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	originalLFS, err := store.LFSPath(repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	blockedLFS := filepath.Join(root, "engineering", "service.lfs")
	if err = os.MkdirAll(blockedLFS, 0o750); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(blockedLFS, "blocker"), []byte("occupied"), 0o640); err != nil {
		t.Fatal(err)
	}

	if err = store.RenameRepository(repositoryPath, "service"); err == nil {
		t.Fatal("repository rename unexpectedly succeeded with an occupied LFS destination")
	}
	if _, err = git.PlainOpen(originalGit); err != nil {
		t.Fatalf("Git repository was not rolled back: %v", err)
	}
	if _, err = os.Stat(originalLFS); err != nil {
		t.Fatalf("LFS repository moved despite rollback: %v", err)
	}
	if _, err = os.Stat(filepath.Join(root, "engineering", "service.git")); !os.IsNotExist(err) {
		t.Fatalf("renamed Git repository remained after rollback: %v", err)
	}
	if contents, readErr := os.ReadFile(filepath.Join(blockedLFS, "blocker")); readErr != nil ||
		string(contents) != "occupied" {
		t.Fatalf("blocked destination changed: %q, %v", contents, readErr)
	}
}

func TestRenameRepositoryRollsBackWhenReviewMoveFails(t *testing.T) {
	root := t.TempDir()
	store := Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	repositoryPath := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	if err := store.CreateRepository(repositoryPath, CreateRepositoryOptions{}); err != nil {
		t.Fatal(err)
	}
	request := review.MergeRequest{
		Title:      "Keep this review",
		Target:     "main",
		Source:     "feature",
		Author:     "alice",
		BaseCommit: strings.Repeat("1", 40),
		HeadCommit: strings.Repeat("2", 40),
	}
	if err := review.NewStore(root).Create(repositoryPath, &request); err != nil {
		t.Fatal(err)
	}
	blockedReview := filepath.Join(root, "engineering", "service.reviews")
	if err := os.MkdirAll(blockedReview, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(blockedReview, "blocker"),
		[]byte("occupied"),
		0o640,
	); err != nil {
		t.Fatal(err)
	}

	if err := store.RenameRepository(repositoryPath, "service"); err == nil {
		t.Fatal("repository rename unexpectedly succeeded with an occupied review destination")
	}
	originalGit, err := store.GitPath(repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = git.PlainOpen(originalGit); err != nil {
		t.Fatalf("Git repository was not rolled back: %v", err)
	}
	persisted, err := review.NewStore(root).Get(repositoryPath, request.ID)
	if err != nil {
		t.Fatalf("original review store was not preserved: %v", err)
	}
	if persisted.Repository != repositoryPath.Full() {
		t.Fatalf("original review was rewritten after rollback: %#v", persisted)
	}
	if contents, readErr := os.ReadFile(filepath.Join(blockedReview, "blocker")); readErr != nil ||
		string(contents) != "occupied" {
		t.Fatalf("blocked review destination changed: %q, %v", contents, readErr)
	}
}

func TestCreateRepositoryRejectsOrphanedReviewData(t *testing.T) {
	root := t.TempDir()
	store := Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	repository := repopath.Repository{
		Groups: []string{"engineering"},
		Name:   "api",
	}
	reviewPath, err := store.ReviewPath(repository)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(reviewPath, 0o750); err != nil {
		t.Fatal(err)
	}

	err = store.CreateRepository(repository, CreateRepositoryOptions{
		InitializeReadme: true,
		Author:           "alice",
	})
	if err == nil {
		t.Fatal("repository creation reused orphaned review data")
	}
	gitPath, pathErr := store.GitPath(repository)
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	if _, statErr := os.Stat(gitPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("repository Git data was created despite orphaned reviews: %v", statErr)
	}
}

func TestCreateRepositoryRejectsDanglingSidecarSymlink(t *testing.T) {
	root := t.TempDir()
	store := Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	repository := repopath.Repository{
		Groups: []string{"engineering"},
		Name:   "api",
	}
	reviewPath, err := store.ReviewPath(repository)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(filepath.Join(root, "missing-reviews"), reviewPath); err != nil {
		t.Fatal(err)
	}

	if err = store.CreateRepository(repository, CreateRepositoryOptions{}); err == nil {
		t.Fatal("repository creation ignored a dangling review sidecar")
	}
	gitPath, err := store.GitPath(repository)
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Lstat(gitPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("Git repository was created despite dangling reviews: %v", statErr)
	}
}

func TestRenameRepositoryRejectsOrphanedDestinationSidecars(t *testing.T) {
	for _, test := range []struct {
		name   string
		create func(string, Store, repopath.Repository) error
	}{
		{
			name: "build directory",
			create: func(_ string, store Store, repository repopath.Repository) error {
				path, err := store.BuildPath(repository)
				if err != nil {
					return err
				}
				return os.MkdirAll(path, 0o750)
			},
		},
		{
			name: "dangling review symlink",
			create: func(root string, store Store, repository repopath.Repository) error {
				path, err := store.ReviewPath(repository)
				if err != nil {
					return err
				}
				return os.Symlink(filepath.Join(root, "missing-reviews"), path)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			store := Store{Root: root}
			if err := store.CreateGroup("engineering", "alice", ""); err != nil {
				t.Fatal(err)
			}
			source := repopath.Repository{
				Groups: []string{"engineering"},
				Name:   "api",
			}
			if err := store.CreateRepository(source, CreateRepositoryOptions{}); err != nil {
				t.Fatal(err)
			}
			destination := repopath.Repository{
				Groups: []string{"engineering"},
				Name:   "service",
			}
			if err := test.create(root, store, destination); err != nil {
				t.Fatal(err)
			}

			if err := store.RenameRepository(source, destination.Name); err == nil {
				t.Fatal("repository rename adopted orphaned destination data")
			}
			sourceGit, err := store.GitPath(source)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = git.PlainOpen(sourceGit); err != nil {
				t.Fatalf("failed rename moved the source repository: %v", err)
			}
			destinationGit, err := store.GitPath(destination)
			if err != nil {
				t.Fatal(err)
			}
			if _, statErr := os.Lstat(destinationGit); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("failed rename created destination Git data: %v", statErr)
			}
		})
	}
}

func TestDeleteRepositoryWaitsForRepositoryOperationLock(t *testing.T) {
	root := t.TempDir()
	store := Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	repository := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	if err := store.CreateRepository(repository, CreateRepositoryOptions{}); err != nil {
		t.Fatal(err)
	}

	acquired := make(chan struct{})
	release := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		unlock, lockErr := review.NewStore(root).AcquireOperationLock()
		if lockErr != nil {
			holderDone <- lockErr
			return
		}
		close(acquired)
		<-release
		holderDone <- unlock()
	}()
	<-acquired
	deleteDone := make(chan error, 1)
	go func() {
		deleteDone <- store.DeleteRepository(repository)
	}()
	select {
	case err := <-deleteDone:
		t.Fatalf("delete bypassed the review lifecycle lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-holderDone; err != nil {
		t.Fatal(err)
	}
	if err := <-deleteDone; err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryMutationsOnDifferentRepositoriesProceedConcurrently(t *testing.T) {
	root := t.TempDir()
	store := Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	api := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	web := repopath.Repository{Groups: []string{"engineering"}, Name: "web"}
	for _, repository := range []repopath.Repository{api, web} {
		if err := store.CreateRepository(repository, CreateRepositoryOptions{}); err != nil {
			t.Fatal(err)
		}
	}

	releaseAPI, err := lockmgr.Process.Acquire(
		lockmgr.RepositoryRequests(root, []repopath.Repository{api}, lockmgr.Exclusive)...,
	)
	if err != nil {
		t.Fatal(err)
	}
	webDeleted := make(chan error, 1)
	go func() {
		webDeleted <- store.DeleteRepository(web)
	}()
	select {
	case err = <-webDeleted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("unrelated repository mutation was blocked")
	}

	apiDeleted := make(chan error, 1)
	go func() {
		apiDeleted <- store.DeleteRepository(api)
	}()
	select {
	case err = <-apiDeleted:
		releaseAPI()
		t.Fatalf("same repository mutation bypassed its lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	releaseAPI()
	select {
	case err = <-apiDeleted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("same repository mutation did not resume")
	}
}

func TestCreateGroupWaitsForRepositoryOperationLock(t *testing.T) {
	root := t.TempDir()
	store := Store{Root: root}
	unlock, err := review.NewStore(root).AcquireOperationLock()
	if err != nil {
		t.Fatal(err)
	}
	createDone := make(chan error, 1)
	go func() {
		createDone <- store.CreateGroup("engineering", "alice", "")
	}()
	select {
	case err = <-createDone:
		t.Fatalf("group creation bypassed the repository operation lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if _, statErr := os.Lstat(filepath.Join(root, "engineering")); !errors.Is(
		statErr,
		os.ErrNotExist,
	) {
		t.Fatalf("group was created while the operation lock was held: %v", statErr)
	}
	if err = unlock(); err != nil {
		t.Fatal(err)
	}
	if err = <-createDone; err != nil {
		t.Fatal(err)
	}
}

func TestRenameGroupWaitsForReviewLifecycleLock(t *testing.T) {
	root := t.TempDir()
	store := Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}

	acquired := make(chan struct{})
	release := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- review.NewStore(root).WithLifecycleLock(func() error {
			close(acquired)
			<-release
			return nil
		})
	}()
	<-acquired
	renameDone := make(chan error, 1)
	go func() {
		renameDone <- store.RenameGroup("engineering", "platform")
	}()
	select {
	case err := <-renameDone:
		t.Fatalf("group rename bypassed the review lifecycle lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := os.Lstat(filepath.Join(root, "platform")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("group moved while lifecycle lock was held: %v", err)
	}
	close(release)
	if err := <-holderDone; err != nil {
		t.Fatal(err)
	}
	if err := <-renameDone; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "platform", "control.git")); err != nil {
		t.Fatalf("group was not moved after releasing lifecycle lock: %v", err)
	}
}

func TestRenameGroupMovesNestedRepositoriesAndRejectsInvalidDestinations(t *testing.T) {
	root := t.TempDir()
	store := Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateGroup("engineering/backend", "alice", ""); err != nil {
		t.Fatal(err)
	}
	repositoryPath := repopath.Repository{
		Groups: []string{"engineering", "backend"},
		Name:   "api",
	}
	if err := store.CreateRepository(
		repositoryPath,
		CreateRepositoryOptions{InitializeReadme: true, Author: "alice"},
	); err != nil {
		t.Fatal(err)
	}
	request := review.MergeRequest{
		Title:      "Move with the group",
		Target:     "main",
		Source:     "feature",
		Author:     "alice",
		BaseCommit: strings.Repeat("1", 40),
		HeadCommit: strings.Repeat("2", 40),
	}
	if err := review.NewStore(root).Create(repositoryPath, &request); err != nil {
		t.Fatal(err)
	}

	if err := store.RenameGroup("engineering", "engineering/backend/moved"); err == nil {
		t.Fatal("group moved into itself")
	}
	if err := store.RenameGroup("engineering", "missing/engineering"); err == nil {
		t.Fatal("group moved below a missing parent")
	}
	if err := store.RenameGroup("engineering", "platform"); err != nil {
		t.Fatal(err)
	}
	if _, err := git.PlainOpen(filepath.Join(root, "platform", "backend", "api.git")); err != nil {
		t.Fatalf("nested repository did not move with group: %v", err)
	}
	renamedRepository := repopath.Repository{
		Groups: []string{"platform", "backend"},
		Name:   "api",
	}
	movedRequest, err := review.NewStore(root).Get(renamedRepository, request.ID)
	if err != nil {
		t.Fatalf("load merge request after group rename: %v", err)
	}
	if movedRequest.Repository != renamedRepository.Full() {
		t.Fatalf("group rename left stale review metadata: %#v", movedRequest)
	}
	if _, err := os.Stat(filepath.Join(root, "engineering")); !os.IsNotExist(err) {
		t.Fatalf("old group path still exists: %v", err)
	}

	if err := store.CreateGroup("destination", "alice", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.RenameGroup("platform", "destination"); err == nil {
		t.Fatal("group replaced an existing destination")
	}
	if _, err := git.PlainOpen(filepath.Join(root, "platform", "backend", "api.git")); err != nil {
		t.Fatalf("failed group rename moved nested repository: %v", err)
	}
}

func TestRepositoryDescriptionHandlesMissingInvalidAndBrokenMetadata(t *testing.T) {
	root := t.TempDir()
	store := Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}

	readmePath := repopath.Repository{Groups: []string{"engineering"}, Name: "readme"}
	if err := store.CreateRepository(readmePath, CreateRepositoryOptions{
		InitializeReadme: true,
		Author:           "alice",
	}); err != nil {
		t.Fatal(err)
	}
	if description, err := store.RepositoryDescription(readmePath); err != nil || description != "" {
		t.Fatalf("README-only description = %q, %v", description, err)
	}

	invalidPath := repopath.Repository{Groups: []string{"engineering"}, Name: "invalid"}
	if err := store.CreateRepository(invalidPath, CreateRepositoryOptions{
		Description: "valid initially",
		Author:      "alice",
	}); err != nil {
		t.Fatal(err)
	}
	gitPath, err := store.GitPath(invalidPath)
	if err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(t.TempDir(), "invalid")
	repository, err := git.PlainClone(checkout, false, &git.CloneOptions{URL: gitPath})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(checkout, ".gitone.yaml"), []byte("{invalid"), 0o640); err != nil {
		t.Fatal(err)
	}
	worktree, err := repository.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = worktree.Add(".gitone.yaml"); err != nil {
		t.Fatal(err)
	}
	if _, err = worktree.Commit("Break metadata", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "alice",
			Email: "alice@localhost",
			When:  time.Now().UTC(),
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err = repository.Push(&git.PushOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.RepositoryDescription(invalidPath); err == nil ||
		!strings.Contains(err.Error(), "read .gitone.yaml") {
		t.Fatalf("invalid metadata error = %v", err)
	}

	bare, err := git.PlainOpen(gitPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = bare.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("main"),
		plumbing.NewHash("4444444444444444444444444444444444444444"),
	)); err != nil {
		t.Fatal(err)
	}
	if _, err = store.RepositoryDescription(invalidPath); err == nil {
		t.Fatal("description accepted a branch pointing to a missing commit")
	}
}

func TestUpdateGroupControlRejectsInvalidStateAndUsesDefaultAuthor(t *testing.T) {
	root := t.TempDir()
	store := Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	controls := control.NewStore(root)
	document, err := controls.Load(context.Background(), "engineering")
	if err != nil {
		t.Fatal(err)
	}
	invalid := document
	invalid.Members = map[string]control.Role{"alice": control.RoleRead}
	if err = store.UpdateGroupControl("engineering", invalid, "alice"); err == nil {
		t.Fatal("invalid control document was committed")
	}
	if err = store.UpdateGroupControl("missing", document, "alice"); err == nil {
		t.Fatal("control document was written to a missing group")
	}

	document.Description = "Updated without an explicit author"
	if err = store.UpdateGroupControl("engineering", document, ""); err != nil {
		t.Fatal(err)
	}
	repository, err := git.PlainOpen(filepath.Join(root, "engineering", "control.git"))
	if err != nil {
		t.Fatal(err)
	}
	head, err := repository.Head()
	if err != nil {
		t.Fatal(err)
	}
	commit, err := repository.CommitObject(head.Hash())
	if err != nil {
		t.Fatal(err)
	}
	if commit.Author.Name != "GitOne" {
		t.Fatalf("default control author = %q", commit.Author.Name)
	}

	if err = repository.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("main"),
		plumbing.NewHash("5555555555555555555555555555555555555555"),
	)); err != nil {
		t.Fatal(err)
	}
	if err = store.UpdateGroupControl("engineering", document, "alice"); err == nil {
		t.Fatal("control update accepted a missing parent commit")
	}
}

func TestStorageLifecycleErrorBranches(t *testing.T) {
	missingRoot := filepath.Join(t.TempDir(), "missing")
	if groups, err := (Store{Root: missingRoot}).ListGroups(); err != nil || len(groups) != 0 {
		t.Fatalf("missing root groups = %#v, %v", groups, err)
	}
	fileRoot := filepath.Join(t.TempDir(), "root-file")
	if err := os.WriteFile(fileRoot, []byte("not a directory"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := (Store{Root: fileRoot}).ListGroups(); err == nil {
		t.Fatal("file storage root was accepted")
	}

	root := t.TempDir()
	store := Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateGroup("engineering", "alice", ""); err == nil {
		t.Fatal("duplicate group was created")
	}
	repositoryPath := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	if err := store.CreateRepository(repositoryPath, CreateRepositoryOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteGroup("engineering"); err == nil {
		t.Fatal("non-empty group was deleted")
	}
	for _, name := range []string{"", "control", "../outside"} {
		if err := store.RenameRepository(repositoryPath, name); err == nil {
			t.Fatalf("repository was renamed to invalid name %q", name)
		}
	}

	lfsPath, err := store.LFSPath(repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(lfsPath); err != nil {
		t.Fatal(err)
	}
	if err = store.RenameRepository(repositoryPath, "service"); err != nil {
		t.Fatalf("rename Git-only repository: %v", err)
	}
	renamed := repopath.Repository{Groups: []string{"engineering"}, Name: "service"}
	if err = store.DeleteRepository(renamed); err != nil {
		t.Fatalf("delete Git-only repository: %v", err)
	}
	if err = store.DeleteRepository(renamed); err == nil {
		t.Fatal("missing repository was deleted twice")
	}
	if err = store.DeleteGroup("missing"); err == nil {
		t.Fatal("missing group was deleted")
	}
}
