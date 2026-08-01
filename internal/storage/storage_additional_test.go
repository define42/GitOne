package storage

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/define42/GitOne/internal/control"
	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/review"
	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	gittransport "github.com/go-git/go-git/v6/plumbing/transport"
)

func TestRemoteImportErrorMessagesAndUnwrap(t *testing.T) {
	for _, test := range []struct {
		name    string
		cause   error
		message string
	}{
		{"canceled", context.Canceled, "remote repository import was canceled"},
		{"deadline", context.DeadlineExceeded, "remote repository import timed out"},
		{"authentication", gittransport.ErrAuthenticationRequired, "remote repository requires authentication"},
		{"authorization", gittransport.ErrAuthorizationFailed, "remote repository rejected the supplied credentials"},
		{"not found", gittransport.ErrRepositoryNotFound, "remote repository was not found"},
		{"empty", gittransport.ErrEmptyRemoteRepository, "remote repository is empty"},
		{"other", errors.New("network failed"), "could not import the remote repository"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := &RemoteImportError{Err: test.cause}
			if err.Error() != test.message {
				t.Fatalf("Error() = %q", err.Error())
			}
			if !errors.Is(err, test.cause) {
				t.Fatal("remote import error did not unwrap its cause")
			}
		})
	}
}

func TestImportRepositoryLockedAndValidatedVariants(t *testing.T) {
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
	locked := repopath.Repository{Groups: []string{"engineering"}, Name: "locked"}
	if err = store.ImportRepositoryLocked(
		context.Background(),
		locked,
		ImportRepositoryOptions{URL: sourcePath},
	); err != nil {
		t.Fatal(err)
	}
	if err = store.ImportRepositoryLocked(
		context.Background(),
		repopath.Repository{Groups: []string{"engineering"}, Name: "control"},
		ImportRepositoryOptions{URL: sourcePath},
	); err == nil {
		t.Fatal("locked import accepted the reserved control repository")
	}
	if err = store.importRepository(
		context.Background(),
		repopath.Repository{Groups: []string{"missing"}, Name: "api"},
		ImportRepositoryOptions{URL: sourcePath},
	); err == nil {
		t.Fatal("internal import accepted a missing group")
	}
	if err = store.ImportRepositoryValidated(
		context.Background(),
		repopath.Repository{Groups: []string{"engineering"}, Name: "control"},
		ImportRepositoryOptions{URL: sourcePath},
		nil,
	); err == nil {
		t.Fatal("validated import accepted the reserved control repository")
	}
	if err = store.ImportRepositoryValidated(
		context.Background(),
		repopath.Repository{Groups: []string{"missing"}, Name: "api"},
		ImportRepositoryOptions{URL: sourcePath},
		nil,
	); err == nil {
		t.Fatal("validated import accepted a missing group")
	}

	validated := repopath.Repository{Groups: []string{"engineering"}, Name: "validated"}
	if err = store.ImportRepositoryValidated(
		context.Background(),
		validated,
		ImportRepositoryOptions{URL: sourcePath},
		nil,
	); err != nil {
		t.Fatal(err)
	}

	conflicted := repopath.Repository{Groups: []string{"engineering"}, Name: "conflicted"}
	if err = store.ImportRepositoryValidated(
		context.Background(),
		conflicted,
		ImportRepositoryOptions{URL: sourcePath},
		func() error {
			buildPath, pathErr := store.BuildPath(conflicted)
			if pathErr != nil {
				return pathErr
			}
			return os.MkdirAll(buildPath, 0o750)
		},
	); err == nil {
		t.Fatal("import published over sidecar data created during staging")
	}

	remote := httptest.NewServer(http.NotFoundHandler())
	defer remote.Close()
	if err = store.ImportRepositoryLocked(
		context.Background(),
		repopath.Repository{Groups: []string{"engineering"}, Name: "authenticated"},
		ImportRepositoryOptions{
			URL:      remote.URL + "/missing.git",
			Username: "alice",
			Password: "secret",
		},
	); err == nil {
		t.Fatal("authenticated import unexpectedly succeeded")
	}
}

func TestAdoptStagedRemoteRepositoryRollsBackPartialPublication(t *testing.T) {
	root := t.TempDir()
	staged := stagedRemoteRepository{
		root:    filepath.Join(root, "staged"),
		gitPath: filepath.Join(root, "staged", "repository.git"),
		lfsPath: filepath.Join(root, "staged", "repository.lfs"),
	}
	for _, path := range []string{staged.gitPath, staged.lfsPath} {
		if err := os.MkdirAll(path, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "marker"), []byte("staged"), 0o640); err != nil {
			t.Fatal(err)
		}
	}

	gitPath := filepath.Join(root, "published.git")
	lfsPath := filepath.Join(root, "missing-parent", "published.lfs")
	if err := adoptStagedRemoteRepository(staged, gitPath, lfsPath); err == nil {
		t.Fatal("partial remote import publication succeeded")
	}
	for _, path := range []string{gitPath, lfsPath} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("failed publication left destination %q: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(staged.lfsPath, "marker")); err != nil {
		t.Fatalf("failed LFS rename lost staged data: %v", err)
	}
}

func TestStoragePathListingAndDescriptionFailures(t *testing.T) {
	missingRoot := filepath.Join(t.TempDir(), "missing")
	groups, err := (Store{Root: missingRoot}).ListGroups()
	if err != nil || len(groups) != 0 {
		t.Fatalf("missing storage groups = %#v, %v", groups, err)
	}

	rootFile := filepath.Join(t.TempDir(), "root-file")
	if err = os.WriteFile(rootFile, []byte("not a directory"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err = (Store{Root: rootFile}).ListGroups(); err == nil {
		t.Fatal("regular file storage root was listed")
	}

	store := Store{Root: t.TempDir()}
	if _, err = store.RepositoryDescription(repopath.Repository{
		Groups: []string{".."},
		Name:   "api",
	}); err == nil {
		t.Fatal("description accepted an invalid repository path")
	}
	if _, err = store.RepositoryDescription(repopath.Repository{
		Groups: []string{"engineering"},
		Name:   "missing",
	}); err == nil {
		t.Fatal("description accepted a missing repository")
	}

	if err = store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	repository := repopath.Repository{Groups: []string{"engineering"}, Name: "bad-head"}
	if err = store.CreateRepository(repository, CreateRepositoryOptions{}); err != nil {
		t.Fatal(err)
	}
	gitPath, err := store.GitPath(repository)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(gitPath, "HEAD"), []byte("not a reference\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err = store.RepositoryDescription(repository); err == nil {
		t.Fatal("description accepted a malformed HEAD")
	}
}

func TestCreateRepositoryAndGroupDefensiveBranches(t *testing.T) {
	root := t.TempDir()
	store := Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRepository(
		repopath.Repository{Groups: []string{"engineering"}, Name: "default-author"},
		CreateRepositoryOptions{InitializeReadme: true, Description: "Both"},
	); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRepositoryLocked(
		repopath.Repository{Groups: []string{"engineering"}, Name: "control"},
		CreateRepositoryOptions{},
	); err == nil {
		t.Fatal("locked repository creation accepted the reserved name")
	}
	if err := store.createRepository(
		repopath.Repository{Groups: []string{".."}, Name: "api"},
		CreateRepositoryOptions{},
	); err == nil {
		t.Fatal("repository creation accepted an invalid group")
	}
	if err := store.createRepository(
		repopath.Repository{Groups: []string{"engineering"}, Name: "../../../escape"},
		CreateRepositoryOptions{},
	); err == nil {
		t.Fatal("repository creation accepted an escaping Git path")
	}

	missingRoot := Store{Root: filepath.Join(t.TempDir(), "missing")}
	if err := missingRoot.createInitializedRepository(
		filepath.Join(missingRoot.Root, "api.git"),
		"api",
		CreateRepositoryOptions{InitializeReadme: true},
	); err == nil {
		t.Fatal("initialized repository was created below a missing root")
	}
	destination := filepath.Join(root, "occupied.git")
	if err := os.MkdirAll(destination, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := store.createInitializedRepository(
		destination,
		"occupied",
		CreateRepositoryOptions{InitializeReadme: true},
	); err == nil {
		t.Fatal("initialized repository replaced its destination")
	}
	if err := store.createInitializedRepository(
		filepath.Join(root, "empty.git"),
		"empty",
		CreateRepositoryOptions{},
	); err == nil {
		t.Fatal("initialized repository created an empty initial commit")
	}

	if err := store.CreateGroupLocked("../invalid", "alice", ""); err == nil {
		t.Fatal("locked group creation accepted an invalid path")
	}
	blockedRoot := filepath.Join(t.TempDir(), "blocked-root")
	if err := os.WriteFile(blockedRoot, []byte("blocked"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := (Store{Root: blockedRoot}).CreateGroupLocked(
		"engineering",
		"alice",
		"",
	); err == nil {
		t.Fatal("group was created below a regular file root")
	}
}

func TestUpdateGroupControlMissingAndBrokenRepositories(t *testing.T) {
	root := t.TempDir()
	store := Store{Root: root}
	document := control.Document{
		Version:    control.CurrentVersion,
		Group:      "missing",
		Inherit:    true,
		Visibility: "private",
		LFS:        control.LFSPolicy{Enabled: true},
		Members:    map[string]control.Role{"alice": control.RoleOwner},
		Tokens:     []control.Token{},
	}
	if err := store.UpdateGroupControlLocked("missing", document, "alice"); err == nil {
		t.Fatal("control update accepted a missing group")
	}
	if err := store.updateGroupControl("../invalid", document, "alice"); err == nil {
		t.Fatal("control update accepted an invalid group path")
	}

	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	document.Group = "engineering"
	controlPath := filepath.Join(root, "engineering", "control.git")
	if err := os.Remove(filepath.Join(controlPath, "refs", "heads", "main")); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateGroupControlLocked("engineering", document, "alice"); err == nil {
		t.Fatal("control update accepted a repository without main")
	}
}

func TestDeleteAndRenameMissingOrInvalidResources(t *testing.T) {
	root := t.TempDir()
	store := Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	missing := repopath.Repository{Groups: []string{"engineering"}, Name: "missing"}
	if err := store.DeleteRepositoryLocked(missing); err == nil {
		t.Fatal("missing repository was deleted")
	}
	if err := store.DeleteRepositoryLocked(repopath.Repository{
		Groups: []string{".."},
		Name:   "api",
	}); err == nil {
		t.Fatal("repository deletion accepted an invalid path")
	}
	if err := store.RenameRepositoryLocked(missing, ""); err == nil {
		t.Fatal("locked rename accepted an empty name")
	}
	if err := store.RenameRepositoryLocked(repopath.Repository{
		Groups: []string{".."},
		Name:   "api",
	}, "service"); err == nil {
		t.Fatal("locked rename accepted an invalid source path")
	}
	if err := store.RenameRepositoryLocked(missing, "service"); err == nil {
		t.Fatal("missing repository was renamed")
	}

	repository := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	if err := store.CreateRepository(repository, CreateRepositoryOptions{}); err != nil {
		t.Fatal(err)
	}
	reviewPath, err := store.ReviewPath(repository)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(filepath.Join(root, "missing-review-target"), reviewPath); err != nil {
		t.Fatal(err)
	}
	if err = store.RenameRepositoryLocked(repository, "service"); err == nil {
		t.Fatal("repository rename accepted a review-store symlink")
	}
}

func TestDeleteAndRenameGroupDefensiveBranches(t *testing.T) {
	store := Store{Root: t.TempDir()}
	if err := store.DeleteGroupLocked("../invalid"); err == nil {
		t.Fatal("group deletion accepted an invalid path")
	}
	if err := store.DeleteGroupLocked("missing"); err == nil {
		t.Fatal("missing group was deleted")
	}
	if err := store.RenameGroupLocked("same", "same"); err == nil {
		t.Fatal("group was renamed onto itself")
	}
	if err := store.RenameGroupLocked("../invalid", "target"); err == nil {
		t.Fatal("group rename accepted an invalid source")
	}
	if err := store.RenameGroupLocked("source", "../invalid"); err == nil {
		t.Fatal("group rename accepted an invalid destination")
	}
	if err := store.RenameGroupLocked("missing", "renamed"); err == nil {
		t.Fatal("missing group was renamed")
	}
}

func TestListGroupsAndPathPermissionFailures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission behavior is required")
	}

	t.Run("storage root stat", func(t *testing.T) {
		parent := t.TempDir()
		root := filepath.Join(parent, "storage")
		if err := os.Mkdir(root, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(parent, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(parent, 0o750) })
		if _, err := (Store{Root: root}).ListGroups(); err == nil {
			t.Fatal("inaccessible storage root was listed")
		}
	})

	t.Run("nested group walk", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "ordinary-file"), []byte("file"), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(root, ".hidden"), 0o750); err != nil {
			t.Fatal(err)
		}
		blocked := filepath.Join(root, "blocked")
		if err := os.Mkdir(blocked, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(blocked, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(blocked, 0o750) })
		if _, err := (Store{Root: root}).ListGroups(); err == nil {
			t.Fatal("inaccessible nested group was walked")
		}
	})

	t.Run("path entry", func(t *testing.T) {
		root := t.TempDir()
		blocked := filepath.Join(root, "blocked")
		if err := os.Mkdir(blocked, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(blocked, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(blocked, 0o750) })
		if _, err := pathEntryExists(filepath.Join(blocked, "entry")); err == nil {
			t.Fatal("inaccessible path entry did not return an error")
		}
	})
}

func TestRepositoryCreationFilesystemFailures(t *testing.T) {
	root := t.TempDir()
	store := Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "engineering", "component"), []byte("file"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := store.createRepository(
		repopath.Repository{Groups: []string{"engineering"}, Name: "component/api"},
		CreateRepositoryOptions{},
	); err == nil {
		t.Fatal("repository creation ignored a non-directory path component")
	}
	if err := store.createRepository(
		repopath.Repository{Groups: []string{"engineering"}, Name: "nested/api"},
		CreateRepositoryOptions{InitializeReadme: true},
	); err == nil {
		t.Fatal("initialized repository was created below a missing directory")
	}
	if err := os.Symlink(
		filepath.Join(root, "missing-component"),
		filepath.Join(root, "engineering", "dangling"),
	); err != nil {
		t.Fatal(err)
	}
	if err := store.createRepository(
		repopath.Repository{Groups: []string{"engineering"}, Name: "dangling/api"},
		CreateRepositoryOptions{},
	); err == nil {
		t.Fatal("repository was initialized through a dangling path component")
	}
	blockedRoot := filepath.Join(t.TempDir(), "root-file")
	if err := os.WriteFile(blockedRoot, []byte("blocked"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := (Store{Root: blockedRoot}).stageRemoteRepository(
		context.Background(),
		ImportRepositoryOptions{URL: "unused"},
	); err == nil {
		t.Fatal("remote repository was staged below a regular file")
	}
}

func TestUpdateGroupControlEmptyAndBrokenTrees(t *testing.T) {
	makeDocument := func(group string) control.Document {
		return control.Document{
			Version:    control.CurrentVersion,
			Group:      group,
			Inherit:    true,
			Visibility: "private",
			LFS:        control.LFSPolicy{Enabled: true},
			Members:    map[string]control.Role{"alice": control.RoleOwner},
			Tokens:     []control.Token{},
		}
	}

	t.Run("missing control file", func(t *testing.T) {
		root := t.TempDir()
		store := Store{Root: root}
		if err := store.CreateGroup("engineering", "alice", ""); err != nil {
			t.Fatal(err)
		}
		repository := openControlRepository(t, root, "engineering")
		emptyTree := &object.Tree{}
		encodedTree := repository.Storer.NewEncodedObject()
		if err := emptyTree.Encode(encodedTree); err != nil {
			t.Fatal(err)
		}
		treeHash, err := repository.Storer.SetEncodedObject(encodedTree)
		if err != nil {
			t.Fatal(err)
		}
		setControlCommit(t, repository, treeHash)
		if err = store.UpdateGroupControlLocked(
			"engineering",
			makeDocument("engineering"),
			"",
		); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("missing tree object", func(t *testing.T) {
		root := t.TempDir()
		store := Store{Root: root}
		if err := store.CreateGroup("engineering", "alice", ""); err != nil {
			t.Fatal(err)
		}
		repository := openControlRepository(t, root, "engineering")
		setControlCommit(
			t,
			repository,
			plumbing.NewHash(strings.Repeat("1", 64)),
		)
		if err := store.UpdateGroupControlLocked(
			"engineering",
			makeDocument("engineering"),
			"alice",
		); err == nil {
			t.Fatal("control update accepted a commit with a missing tree")
		}
	})

	t.Run("read-only object store", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("POSIX permission behavior is required")
		}
		root := t.TempDir()
		store := Store{Root: root}
		if err := store.CreateGroup("engineering", "alice", ""); err != nil {
			t.Fatal(err)
		}
		objects := filepath.Join(root, "engineering", "control.git", "objects")
		if err := filepath.Walk(objects, func(
			path string,
			info os.FileInfo,
			walkErr error,
		) error {
			if walkErr != nil {
				return walkErr
			}
			if info.IsDir() {
				return os.Chmod(path, 0o500)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = filepath.Walk(objects, func(
				path string,
				info os.FileInfo,
				walkErr error,
			) error {
				if walkErr == nil && info.IsDir() {
					return os.Chmod(path, 0o750)
				}
				return nil
			})
		})
		if err := store.UpdateGroupControlLocked(
			"engineering",
			makeDocument("engineering"),
			"alice",
		); err == nil {
			t.Fatal("control update wrote to a read-only object store")
		}
	})
}

func TestRepositoryAndGroupTrashFailures(t *testing.T) {
	t.Run("repository trash", func(t *testing.T) {
		root := t.TempDir()
		store := Store{Root: root}
		if err := store.CreateGroup("engineering", "alice", ""); err != nil {
			t.Fatal(err)
		}
		repository := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
		if err := store.CreateRepository(repository, CreateRepositoryOptions{}); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".trash"), []byte("blocked"), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := store.DeleteRepositoryLocked(repository); err == nil {
			t.Fatal("repository deletion ignored an invalid trash path")
		}
	})

	t.Run("group trash", func(t *testing.T) {
		root := t.TempDir()
		store := Store{Root: root}
		if err := store.CreateGroup("engineering", "alice", ""); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".trash"), []byte("blocked"), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := store.DeleteGroupLocked("engineering"); err == nil {
			t.Fatal("group deletion ignored an invalid trash path")
		}
	})
}

func TestRepositoryMutationPermissionFailures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission behavior is required")
	}
	root := t.TempDir()
	store := Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	repository := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	if err := store.CreateRepository(repository, CreateRepositoryOptions{}); err != nil {
		t.Fatal(err)
	}
	groupPath := filepath.Join(root, "engineering")
	if err := os.Chmod(groupPath, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(groupPath, 0o750) })
	if err := store.DeleteRepositoryLocked(repository); err == nil {
		t.Fatal("repository deletion ignored an inaccessible sidecar")
	}
	if err := store.RenameRepositoryLocked(repository, "service"); err == nil {
		t.Fatal("repository rename ignored an inaccessible destination")
	}
}

func TestRepositoryRenameSourceSidecarPermissionFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission behavior is required")
	}
	root := t.TempDir()
	store := Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	component := filepath.Join(root, "engineering", "component")
	if err := os.Mkdir(component, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(component, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(component, 0o750) })
	if err := store.RenameRepositoryLocked(repopath.Repository{
		Groups: []string{"engineering"},
		Name:   "component/api",
	}, "service"); err == nil {
		t.Fatal("repository rename ignored an inaccessible source sidecar")
	}
}

func TestRepositoryRenameRollsBackAllMovedSidecars(t *testing.T) {
	root := t.TempDir()
	store := Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	repository := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	if err := store.CreateRepository(repository, CreateRepositoryOptions{}); err != nil {
		t.Fatal(err)
	}
	buildPath, err := store.BuildPath(repository)
	if err != nil {
		t.Fatal(err)
	}
	lfsPath, err := store.LFSPath(repository)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(lfsPath, 0o750); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(buildPath, 0o750); err != nil {
		t.Fatal(err)
	}
	reviewPath, err := store.ReviewPath(repository)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(reviewPath, 0o750); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(reviewPath, "1.json"), []byte("{broken"), 0o640); err != nil {
		t.Fatal(err)
	}

	if err = store.RenameRepositoryLocked(repository, "service"); err == nil {
		t.Fatal("repository rename accepted malformed review state")
	}
	for _, original := range []string{
		filepath.Join(root, "engineering", "api.git"),
		filepath.Join(root, "engineering", "api.lfs"),
		filepath.Join(root, "engineering", "api.build"),
		filepath.Join(root, "engineering", "api.reviews"),
	} {
		if _, statErr := os.Stat(original); statErr != nil {
			t.Fatalf("rollback did not restore %s: %v", original, statErr)
		}
	}
}

func TestRepositoryRenameReportsRollbackFailures(t *testing.T) {
	root := t.TempDir()
	store := Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	repository := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	if err := store.CreateRepository(repository, CreateRepositoryOptions{}); err != nil {
		t.Fatal(err)
	}
	buildPath, err := store.BuildPath(repository)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(buildPath, 0o750); err != nil {
		t.Fatal(err)
	}
	reviewPath, err := store.ReviewPath(repository)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(reviewPath, 0o750); err != nil {
		t.Fatal(err)
	}
	request := review.MergeRequest{
		Title:             "Slow relocation",
		Description:       strings.Repeat("x", 7<<20),
		Target:            "main",
		Source:            "feature",
		Author:            "alice",
		BaseCommit:        strings.Repeat("0", 64),
		HeadCommit:        strings.Repeat("1", 64),
		RequiredApprovals: 1,
		Approvals:         []review.Approval{},
		Threads:           []review.Thread{},
	}
	if err = review.NewStore(root).Create(repository, &request); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(reviewPath, "2.json"), []byte("{broken"), 0o640); err != nil {
		t.Fatal(err)
	}

	renameDone := make(chan error, 1)
	go func() {
		renameDone <- store.RenameRepositoryLocked(repository, "service")
	}()
	destinationBuild := filepath.Join(root, "engineering", "service.build")
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, statErr := os.Stat(destinationBuild); statErr == nil {
			break
		}
		select {
		case renameErr := <-renameDone:
			t.Fatalf("repository rename ended before review relocation: %v", renameErr)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("repository sidecars were not moved before review relocation")
		}
		runtime.Gosched()
	}
	for _, original := range []string{
		filepath.Join(root, "engineering", "api.git"),
		filepath.Join(root, "engineering", "api.lfs"),
		filepath.Join(root, "engineering", "api.build"),
	} {
		if err = os.MkdirAll(original, 0o750); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(original, "blocker"), []byte("blocked"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case err = <-renameDone:
		if err == nil || !strings.Contains(err.Error(), "restore repository") {
			t.Fatalf("rename rollback error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("repository rename did not finish")
	}
}

func TestGroupRenameRollsBackMalformedReviews(t *testing.T) {
	root := t.TempDir()
	store := Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	repository := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	if err := store.CreateRepository(repository, CreateRepositoryOptions{}); err != nil {
		t.Fatal(err)
	}
	reviewPath, err := store.ReviewPath(repository)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(reviewPath, 0o750); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(reviewPath, "1.json"), []byte("{broken"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err = store.RenameGroupLocked("engineering", "platform"); err == nil {
		t.Fatal("group rename accepted malformed review state")
	}
	if _, err = os.Stat(filepath.Join(root, "engineering", "control.git")); err != nil {
		t.Fatalf("group rollback did not restore the source: %v", err)
	}
	if _, err = os.Stat(filepath.Join(root, "platform")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("group rollback left destination data: %v", err)
	}
}

func TestGroupRenameDestinationPermissionFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission behavior is required")
	}
	root := t.TempDir()
	store := Store{Root: root}
	if err := store.CreateGroup("source", "alice", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateGroup("blocked", "alice", ""); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(root, "blocked")
	if err := os.Chmod(blocked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o750) })
	if err := store.RenameGroupLocked("source", "blocked/target"); err == nil {
		t.Fatal("group rename ignored an inaccessible destination")
	}
}

func openControlRepository(
	t *testing.T,
	root string,
	group string,
) *git.Repository {
	t.Helper()
	repository, err := git.PlainOpen(filepath.Join(root, group, "control.git"))
	if err != nil {
		t.Fatal(err)
	}
	return repository
}

func setControlCommit(
	t *testing.T,
	repository *git.Repository,
	treeHash plumbing.Hash,
) {
	t.Helper()
	signature := object.Signature{
		Name:  "alice",
		Email: "alice@localhost",
		When:  time.Now().UTC(),
	}
	commit := &object.Commit{
		Author:    signature,
		Committer: signature,
		Message:   "test control state",
		TreeHash:  treeHash,
	}
	encoded := repository.Storer.NewEncodedObject()
	if err := commit.Encode(encoded); err != nil {
		t.Fatal(err)
	}
	hash, err := repository.Storer.SetEncodedObject(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if err = repository.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("main"),
		hash,
	)); err != nil {
		t.Fatal(err)
	}
}
