package storage

import (
	"errors"
	"github.com/define42/GitOne/internal/repopath"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"os"
	"path/filepath"
	"testing"
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
