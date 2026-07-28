package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/define42/GitOne/internal/control"
	"github.com/define42/GitOne/internal/repopath"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
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
	document.Members["bob"] = control.RoleWrite
	document.Tokens = append(document.Tokens, control.Token{
		Name: "deploy",
		Key:  "ci",
		Hash: "sha256:test",
		Role: control.RoleWrite,
	})
	document.Repositories["api"] = control.RepositoryPolicy{
		Visibility: "private",
		LFS: control.LFSPolicy{
			Enabled:             true,
			MaximumObjectBytes:  1024,
			MaximumStorageBytes: 4096,
		},
	}

	if err = store.UpdateGroupControl("engineering", document, "alice"); err != nil {
		t.Fatal(err)
	}
	updated, err := control.NewStore(root).Load(context.Background(), "engineering")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Description != "After" ||
		updated.Inherit ||
		updated.Members["bob"] != control.RoleWrite ||
		len(updated.Tokens) != 1 ||
		!updated.Repositories["api"].LFS.Enabled {
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
	if len(tree.Entries) != 1 || tree.Entries[0].Name != ".gitone.json" {
		t.Fatalf("unexpected description-only tree: %#v", tree.Entries)
	}
	metadata, err := commit.File(".gitone.json")
	if err != nil {
		t.Fatal(err)
	}
	contents, err := metadata.Contents()
	if err != nil {
		t.Fatal(err)
	}
	if contents != "{\n  \"description\": \"Backend API\"\n}\n" {
		t.Fatalf("unexpected .gitone.json contents: %q", contents)
	}
	description, err := store.RepositoryDescription(repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	if description != "Backend API" {
		t.Fatalf("unexpected repository description: %q", description)
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

	if err = store.DeleteRepository(renamedPath); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{renamedGitPath, renamedLFSPath} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("deleted repository path still exists: %s: %v", path, statErr)
		}
	}

	var foundGit, foundPayload bool
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
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !foundGit || !foundPayload {
		t.Fatalf("trash is missing Git or LFS data: git=%v lfs=%v", foundGit, foundPayload)
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

func TestRenameGroupMovesNestedRepositoriesAndRejectsInvalidDestinations(t *testing.T) {
	root := t.TempDir()
	store := Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateGroup("engineering/backend", "alice", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRepository(
		repopath.Repository{Groups: []string{"engineering", "backend"}, Name: "api"},
		CreateRepositoryOptions{InitializeReadme: true, Author: "alice"},
	); err != nil {
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
