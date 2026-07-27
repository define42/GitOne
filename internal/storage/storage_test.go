package storage

import (
	"github.com/define42/GitOne/internal/repopath"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateGroupAndRepository(t *testing.T) {
	root := t.TempDir()
	s := Store{Root: root}
	if e := s.CreateGroup("engineering", "alice"); e != nil {
		t.Fatal(e)
	}
	r := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	if e := s.CreateRepository(r); e != nil {
		t.Fatal(e)
	}
	for _, p := range []string{filepath.Join(root, "engineering", "control.git"), filepath.Join(root, "engineering", "api.git"), filepath.Join(root, "engineering", "api.lfs", "objects")} {
		if _, e := os.Stat(p); e != nil {
			t.Fatalf("missing %s: %v", p, e)
		}
	}
}
func TestSubgroupNeedsParent(t *testing.T) {
	s := Store{Root: t.TempDir()}
	if e := s.CreateGroup("a/b", "alice"); e == nil {
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

	if err = s.CreateGroup("engineering", "alice"); err != nil {
		t.Fatalf("create top-level group: %v", err)
	}
	if _, err = os.Stat(filepath.Join(root, "engineering", "control.git")); err != nil {
		t.Fatalf("control repository was not created: %v", err)
	}
}

func TestListGroupsAndRepositories(t *testing.T) {
	s := Store{Root: t.TempDir()}
	if err := s.CreateGroup("engineering", "alice"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateGroup("engineering/backend", "alice"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRepository(repopath.Repository{Groups: []string{"engineering", "backend"}, Name: "api"}); err != nil {
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
