package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/define42/GitOne/internal/control"
	"github.com/define42/GitOne/internal/repopath"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Store struct{ Root string }

type GroupInfo struct {
	Path         string
	Repositories []string
}

func (s Store) GitPath(r repopath.Repository) (string, error) {
	return repopath.SafeJoin(s.Root, append(r.Groups, r.Name+".git")...)
}
func (s Store) LFSPath(r repopath.Repository) (string, error) {
	return repopath.SafeJoin(s.Root, append(r.Groups, r.Name+".lfs")...)
}
func (s Store) GroupPath(group string) (string, error) {
	parts, e := repopath.ParseGroup(group)
	if e != nil {
		return "", e
	}
	return repopath.SafeJoin(s.Root, parts...)
}

func (s Store) ListGroups() ([]GroupInfo, error) {
	root, err := repopath.SafeJoin(s.Root)
	if err != nil {
		return nil, err
	}
	if _, err = os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return []GroupInfo{}, nil
	} else if err != nil {
		return nil, err
	}

	groups := []GroupInfo{}
	var walk func(string, []string) error
	walk = func(directory string, parts []string) error {
		entries, err := os.ReadDir(directory)
		if err != nil {
			return err
		}

		hasControl := false
		repositories := []string{}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			switch {
			case entry.Name() == "control.git":
				hasControl = true
			case strings.HasSuffix(entry.Name(), ".git"):
				repositories = append(repositories, strings.TrimSuffix(entry.Name(), ".git"))
			}
		}
		if hasControl {
			groups = append(groups, GroupInfo{
				Path:         strings.Join(parts, "/"),
				Repositories: repositories,
			})
		}

		for _, entry := range entries {
			if !entry.IsDir() ||
				strings.HasSuffix(entry.Name(), ".git") ||
				strings.HasSuffix(entry.Name(), ".lfs") {
				continue
			}
			nextParts := append(append([]string(nil), parts...), entry.Name())
			if _, err := repopath.ParseGroup(strings.Join(nextParts, "/")); err != nil {
				continue
			}
			if err := walk(filepath.Join(directory, entry.Name()), nextParts); err != nil {
				return err
			}
		}
		return nil
	}
	if err = walk(root, nil); err != nil {
		return nil, err
	}
	return groups, nil
}

func (s Store) CreateRepository(r repopath.Repository) error {
	if r.Name == "control" {
		return errors.New("reserved repository name")
	}
	gp, e := s.GroupPath(r.Group())
	if e != nil {
		return e
	}
	if _, e = os.Stat(filepath.Join(gp, "control.git")); e != nil {
		return errors.New("group does not exist")
	}
	gitp, _ := s.GitPath(r)
	lfsp, _ := s.LFSPath(r)
	if _, e = os.Stat(gitp); e == nil {
		return errors.New("repository exists")
	}
	if e = os.MkdirAll(gp, 0750); e != nil {
		return e
	}
	if _, e = git.PlainInit(gitp, true); e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Join(lfsp, "objects"), 0750); e != nil {
		_ = os.RemoveAll(gitp)
		return e
	}
	return nil
}
func plumbingSymbolicMain() *plumbing.Reference {
	return plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName("main"))
}
func (s Store) CreateGroup(group, owner string) error {
	gp, e := s.GroupPath(group)
	if e != nil {
		return e
	}
	root, e := repopath.SafeJoin(s.Root)
	if e != nil {
		return e
	}
	if _, e = os.Stat(gp); e == nil {
		return errors.New("group exists")
	}
	if parent := filepath.Dir(gp); parent != root {
		if _, e = os.Stat(filepath.Join(parent, "control.git")); e != nil {
			return errors.New("parent group does not exist")
		}
	}
	if e = os.MkdirAll(gp, 0750); e != nil {
		return e
	}
	tmp, e := os.MkdirTemp(root, ".gitone-control-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(tmp)
	r, e := git.PlainInit(tmp, false)
	if e != nil {
		return e
	}
	wt, e := r.Worktree()
	if e != nil {
		return e
	}
	doc := control.Document{Version: 1, Group: group, Inherit: true, Members: map[string]control.Role{owner: control.RoleOwner}, Tokens: []control.Token{}, Repositories: map[string]control.RepositoryPolicy{}}
	b, _ := json.MarshalIndent(doc, "", "  ")
	b = append(b, '\n')
	if e = os.WriteFile(filepath.Join(tmp, "control.json"), b, 0640); e != nil {
		return e
	}
	if _, e = wt.Add("control.json"); e != nil {
		return e
	}
	commit, e := wt.Commit("Initialize group control", &git.CommitOptions{Author: &object.Signature{Name: owner, Email: owner + "@localhost", When: time.Now().UTC()}})
	if e != nil {
		return e
	}
	if e = r.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName("main"), commit)); e != nil {
		return e
	}
	if e = r.Storer.SetReference(plumbingSymbolicMain()); e != nil {
		return e
	}
	repositoryConfig, e := r.Config()
	if e != nil {
		return e
	}
	repositoryConfig.Core.IsBare = true
	repositoryConfig.Core.Worktree = ""
	if e = r.SetConfig(repositoryConfig); e != nil {
		return e
	}
	dest := filepath.Join(gp, "control.git")
	if e = os.Rename(filepath.Join(tmp, ".git"), dest); e != nil {
		_ = os.RemoveAll(gp)
		return fmt.Errorf("create control repository: %w", e)
	}
	return nil
}

func (s Store) DeleteRepository(r repopath.Repository) error {
	gitp, err := s.GitPath(r)
	if err != nil {
		return err
	}
	lfsp, err := s.LFSPath(r)
	if err != nil {
		return err
	}
	trash, err := repopath.SafeJoin(s.Root, ".trash", time.Now().UTC().Format("20060102T150405.000000000"), r.Group())
	if err != nil {
		return err
	}
	if err = os.MkdirAll(trash, 0750); err != nil {
		return err
	}
	if err = os.Rename(gitp, filepath.Join(trash, r.Name+".git")); err != nil {
		return err
	}
	if _, statErr := os.Stat(lfsp); statErr == nil {
		_ = os.Rename(lfsp, filepath.Join(trash, r.Name+".lfs"))
	}
	return nil
}

func (s Store) RenameRepository(r repopath.Repository, newName string) error {
	if newName == "" || newName == "control" || filepath.Base(newName) != newName {
		return errors.New("invalid repository name")
	}
	gitp, err := s.GitPath(r)
	if err != nil {
		return err
	}
	lfsp, err := s.LFSPath(r)
	if err != nil {
		return err
	}
	dstGit, err := repopath.SafeJoin(s.Root, append(r.Groups, newName+".git")...)
	if err != nil {
		return err
	}
	dstLFS, err := repopath.SafeJoin(s.Root, append(r.Groups, newName+".lfs")...)
	if err != nil {
		return err
	}
	if err = os.Rename(gitp, dstGit); err != nil {
		return err
	}
	if _, statErr := os.Stat(lfsp); statErr == nil {
		if err = os.Rename(lfsp, dstLFS); err != nil {
			_ = os.Rename(dstGit, gitp)
			return err
		}
	}
	return nil
}

func (s Store) DeleteGroup(group string) error {
	gp, err := s.GroupPath(group)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(gp)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() != "control.git" {
			return errors.New("group is not empty")
		}
	}
	trash, err := repopath.SafeJoin(s.Root, ".trash", time.Now().UTC().Format("20060102T150405.000000000"))
	if err != nil {
		return err
	}
	if err = os.MkdirAll(trash, 0750); err != nil {
		return err
	}
	return os.Rename(gp, filepath.Join(trash, strings.ReplaceAll(group, "/", "__")))
}

func (s Store) RenameGroup(group, newPath string) error {
	src, err := s.GroupPath(group)
	if err != nil {
		return err
	}
	dst, err := s.GroupPath(newPath)
	if err != nil {
		return err
	}
	if _, err = os.Stat(dst); err == nil {
		return errors.New("destination group exists")
	}
	if err = os.MkdirAll(filepath.Dir(dst), 0750); err != nil {
		return err
	}
	return os.Rename(src, dst)
}
