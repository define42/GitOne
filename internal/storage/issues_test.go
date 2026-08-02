package storage

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/define42/GitOne/internal/issue"
	"github.com/define42/GitOne/internal/repopath"
	git "github.com/go-git/go-git/v5"
)

func createIssueTestRepository(
	t *testing.T,
	store Store,
	repository repopath.Repository,
) issue.Issue {
	t.Helper()
	if err := store.CreateRepository(repository, CreateRepositoryOptions{}); err != nil {
		t.Fatal(err)
	}
	record := issue.Issue{Title: "Track this", Description: "Details", Author: "alice"}
	if err := issue.NewStore(store.Root).Create(repository, &record); err != nil {
		t.Fatal(err)
	}
	return record
}

func TestRenameRepositoryRelocatesIssues(t *testing.T) {
	root := t.TempDir()
	store := Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	repository := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	record := createIssueTestRepository(t, store, repository)

	if err := store.RenameRepository(repository, "service"); err != nil {
		t.Fatal(err)
	}
	renamed := repopath.Repository{Groups: []string{"engineering"}, Name: "service"}
	moved, err := issue.NewStore(root).Get(renamed, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if moved.Repository != renamed.Full() || moved.Title != record.Title {
		t.Fatalf("unexpected relocated issue: %#v", moved)
	}
	originalIssues, err := store.IssuePath(repository)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(originalIssues); !os.IsNotExist(err) {
		t.Fatalf("original issue store remained: %v", err)
	}
}

func TestRenameRepositoryRollsBackWhenIssueMoveFails(t *testing.T) {
	root := t.TempDir()
	store := Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	repository := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	record := createIssueTestRepository(t, store, repository)

	blocked := filepath.Join(root, "engineering", "service.issues")
	if err := os.MkdirAll(blocked, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocked, "blocker"), []byte("occupied"), 0o640); err != nil {
		t.Fatal(err)
	}

	if err := store.RenameRepository(repository, "service"); err == nil {
		t.Fatal("repository rename unexpectedly succeeded with an occupied issue destination")
	}
	originalGit, err := store.GitPath(repository)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = git.PlainOpen(originalGit); err != nil {
		t.Fatalf("Git repository was not rolled back: %v", err)
	}
	preserved, err := issue.NewStore(root).Get(repository, record.ID)
	if err != nil {
		t.Fatalf("issue was not preserved: %v", err)
	}
	if preserved.Repository != repository.Full() {
		t.Fatalf("issue repository = %q", preserved.Repository)
	}
	if contents, readErr := os.ReadFile(filepath.Join(blocked, "blocker")); readErr != nil ||
		string(contents) != "occupied" {
		t.Fatalf("blocked destination changed: %q, %v", contents, readErr)
	}
}

func TestRenameGroupRewritesIssues(t *testing.T) {
	root := t.TempDir()
	store := Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSubgroupLocked("engineering/backend"); err != nil {
		t.Fatal(err)
	}
	repository := repopath.Repository{
		Groups: []string{"engineering", "backend"},
		Name:   "api",
	}
	record := createIssueTestRepository(t, store, repository)

	if err := store.RenameGroup("engineering/backend", "engineering/platform"); err != nil {
		t.Fatal(err)
	}
	moved := repopath.Repository{Groups: []string{"engineering", "platform"}, Name: "api"}
	rewritten, err := issue.NewStore(root).Get(moved, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rewritten.Repository != moved.Full() {
		t.Fatalf("rewritten issue repository = %q", rewritten.Repository)
	}
}

func TestDeleteRepositoryTrashesIssues(t *testing.T) {
	root := t.TempDir()
	store := Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	repository := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	createIssueTestRepository(t, store, repository)
	issuePath, err := store.IssuePath(repository)
	if err != nil {
		t.Fatal(err)
	}

	if err = store.DeleteRepository(repository); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(issuePath); !os.IsNotExist(err) {
		t.Fatalf("issue store remained after deletion: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(root, ".trash", "*", "engineering", "api.issues"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("trashed issue stores = %v, want one", matches)
	}
}

func TestCreateRepositoryRejectsExistingIssueStore(t *testing.T) {
	root := t.TempDir()
	store := Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "engineering", "api.issues"), 0o750); err != nil {
		t.Fatal(err)
	}
	repository := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	if err := store.CreateRepository(repository, CreateRepositoryOptions{}); err == nil {
		t.Fatal("expected repository creation to fail with an existing issue store")
	}
}

func TestReservedGroupDirectoryCoversIssueStores(t *testing.T) {
	if !reservedGroupDirectory("api.issues") {
		t.Fatal("issue stores must be reserved storage directories")
	}
}
