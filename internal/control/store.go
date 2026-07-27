package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"io"
	"path/filepath"
	"sync"
)

type Store struct {
	Root  string
	mu    sync.RWMutex
	cache map[string]cached
}
type cached struct {
	hash plumbing.Hash
	doc  Document
}

func NewStore(root string) *Store { return &Store{Root: root, cache: map[string]cached{}} }
func (s *Store) Load(ctx context.Context, group string) (Document, error) {
	_ = ctx
	p := filepath.Join(s.Root, filepath.FromSlash(group), "control.git")
	r, e := git.PlainOpen(p)
	if e != nil {
		return Document{}, e
	}
	ref, e := r.Reference(plumbing.NewBranchReferenceName("main"), true)
	if e != nil {
		return Document{}, e
	}
	s.mu.RLock()
	c, ok := s.cache[group]
	s.mu.RUnlock()
	if ok && c.hash == ref.Hash() {
		return c.doc, nil
	}
	commit, e := r.CommitObject(ref.Hash())
	if e != nil {
		return Document{}, e
	}
	f, e := commit.File("control.json")
	if e != nil {
		return Document{}, e
	}
	rd, e := f.Reader()
	if e != nil {
		return Document{}, e
	}
	defer rd.Close()
	dec := json.NewDecoder(io.LimitReader(rd, 1<<20))
	dec.DisallowUnknownFields()
	var d Document
	if e = dec.Decode(&d); e != nil {
		return d, e
	}
	if e = Validate(group, d); e != nil {
		return d, e
	}
	s.mu.Lock()
	s.cache[group] = cached{ref.Hash(), d}
	s.mu.Unlock()
	return d, nil
}
func Validate(group string, d Document) error {
	if d.Version != 1 {
		return fmt.Errorf("unsupported version")
	}
	if d.Group != group {
		return fmt.Errorf("group mismatch")
	}
	owners := 0
	for _, r := range d.Members {
		if r == RoleOwner {
			owners++
		}
		if !validRole(r) {
			return fmt.Errorf("invalid role")
		}
	}
	if owners == 0 {
		return errors.New("at least one owner required")
	}
	for name := range d.Repositories {
		if name == "control" || name == "" || filepath.Base(name) != name {
			return fmt.Errorf("invalid repository name %q", name)
		}
	}
	return nil
}
func validRole(r Role) bool {
	return r == RoleRead || r == RoleWrite || r == RoleAdmin || r == RoleOwner
}
func (s *Store) Invalidate(group string) { s.mu.Lock(); delete(s.cache, group); s.mu.Unlock() }
