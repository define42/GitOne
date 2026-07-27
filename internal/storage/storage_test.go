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
