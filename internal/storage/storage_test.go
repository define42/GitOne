package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"example.com/puregit-server/internal/repopath"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateGroupAndRepository(t *testing.T) {
	root := t.TempDir()
	s := Store{Root: root}
	sum := sha256.Sum256([]byte("secret"))
	if e := s.CreateGroup("engineering", "alice", "alice", "sha256:"+hex.EncodeToString(sum[:])); e != nil {
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
	if e := s.CreateGroup("a/b", "alice", "alice", "sha256:x"); e == nil {
		t.Fatal("expected parent error")
	}
}
