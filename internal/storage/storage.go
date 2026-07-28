package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/define42/GitOne/internal/control"
	"github.com/define42/GitOne/internal/repoconfig"
	"github.com/define42/GitOne/internal/repopath"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

type Store struct{ Root string }

type CreateRepositoryOptions struct {
	InitializeReadme bool
	Author           string
	Description      string
}

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

func (s Store) BuildPath(r repopath.Repository) (string, error) {
	return repopath.SafeJoin(s.Root, append(r.Groups, r.Name+".build")...)
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
				strings.HasSuffix(entry.Name(), ".lfs") ||
				strings.HasSuffix(entry.Name(), ".build") {
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

func (s Store) CreateRepository(r repopath.Repository, options CreateRepositoryOptions) error {
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
	if e = os.MkdirAll(gp, 0o750); e != nil {
		return e
	}
	if options.InitializeReadme || options.Description != "" {
		if e = s.createInitializedRepository(gitp, r.Name, options); e != nil {
			return e
		}
	} else {
		repository, initErr := git.PlainInit(gitp, true)
		if initErr != nil {
			return initErr
		}
		if e = repository.Storer.SetReference(plumbingSymbolicMain()); e != nil {
			_ = os.RemoveAll(gitp)
			return e
		}
	}
	if e = os.MkdirAll(filepath.Join(lfsp, "objects"), 0o750); e != nil {
		_ = os.RemoveAll(gitp)
		return e
	}
	return nil
}

func (s Store) RepositoryDescription(r repopath.Repository) (string, error) {
	path, err := s.GitPath(r)
	if err != nil {
		return "", err
	}
	repository, err := git.PlainOpen(path)
	if err != nil {
		return "", err
	}
	head, err := repository.Head()
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	metadata, found, err := repoconfig.Read(repository, head.Hash())
	if err != nil {
		return "", err
	}
	if !found {
		return "", nil
	}
	return metadata.Description, nil
}

func (s Store) createInitializedRepository(destination, name string, options CreateRepositoryOptions) error {
	root, err := repopath.SafeJoin(s.Root)
	if err != nil {
		return err
	}
	temporary, err := os.MkdirTemp(root, ".gitone-repository-")
	if err != nil {
		return err
	}
	defer func() {
		_ = os.RemoveAll(temporary)
	}()

	repository, err := git.PlainInit(temporary, false)
	if err != nil {
		return err
	}
	if err = repository.Storer.SetReference(plumbingSymbolicMain()); err != nil {
		return err
	}
	worktree, err := repository.Worktree()
	if err != nil {
		return err
	}
	if options.InitializeReadme {
		if err = os.WriteFile(filepath.Join(temporary, "README.md"), []byte(name+"\n"), 0o640); err != nil {
			return err
		}
		if _, err = worktree.Add("README.md"); err != nil {
			return err
		}
	}
	if options.Description != "" {
		metadata, marshalErr := json.MarshalIndent(repoconfig.Config{Description: options.Description}, "", "  ")
		if marshalErr != nil {
			return marshalErr
		}
		metadata = append(metadata, '\n')
		if err = os.WriteFile(filepath.Join(temporary, ".gitone.json"), metadata, 0o640); err != nil {
			return err
		}
		if _, err = worktree.Add(".gitone.json"); err != nil {
			return err
		}
	}
	if options.Author == "" {
		options.Author = "GitOne"
	}
	commit, err := worktree.Commit("Initialize repository", &git.CommitOptions{
		Author: &object.Signature{
			Name:  options.Author,
			Email: options.Author + "@localhost",
			When:  time.Now().UTC(),
		},
	})
	if err != nil {
		return err
	}
	if err = repository.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName("main"), commit)); err != nil {
		return err
	}
	if err = repository.Storer.SetReference(plumbingSymbolicMain()); err != nil {
		return err
	}
	repositoryConfig, err := repository.Config()
	if err != nil {
		return err
	}
	repositoryConfig.Core.IsBare = true
	repositoryConfig.Core.Worktree = ""
	if err = repository.SetConfig(repositoryConfig); err != nil {
		return err
	}
	if err = os.Rename(filepath.Join(temporary, ".git"), destination); err != nil {
		return fmt.Errorf("create initialized repository: %w", err)
	}
	return nil
}

func plumbingSymbolicMain() *plumbing.Reference {
	return plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName("main"))
}

func (s Store) CreateGroup(group, owner, description string) error {
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
	if e = os.MkdirAll(gp, 0o750); e != nil {
		return e
	}
	tmp, e := os.MkdirTemp(root, ".gitone-control-")
	if e != nil {
		return e
	}
	defer func() {
		_ = os.RemoveAll(tmp)
	}()
	r, e := git.PlainInit(tmp, false)
	if e != nil {
		return e
	}
	if e = r.Storer.SetReference(plumbingSymbolicMain()); e != nil {
		return e
	}
	wt, e := r.Worktree()
	if e != nil {
		return e
	}
	doc := control.Document{
		Version:      1,
		Group:        group,
		Description:  description,
		Inherit:      true,
		Members:      map[string]control.Role{owner: control.RoleOwner},
		Tokens:       []control.Token{},
		Repositories: map[string]control.RepositoryPolicy{},
	}
	b, _ := json.MarshalIndent(doc, "", "  ")
	b = append(b, '\n')
	if e = os.WriteFile(filepath.Join(tmp, "control.json"), b, 0o640); e != nil {
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

func (s Store) UpdateGroupControl(group string, document control.Document, author string) error {
	if err := control.Validate(group, document); err != nil {
		return err
	}
	groupPath, err := s.GroupPath(group)
	if err != nil {
		return err
	}
	repository, err := git.PlainOpen(filepath.Join(groupPath, "control.git"))
	if err != nil {
		return err
	}
	head, err := repository.Reference(plumbing.NewBranchReferenceName("main"), true)
	if err != nil {
		return err
	}
	parent, err := repository.CommitObject(head.Hash())
	if err != nil {
		return err
	}
	tree, err := parent.Tree()
	if err != nil {
		return err
	}

	contents, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	blob := &plumbing.MemoryObject{}
	blob.SetType(plumbing.BlobObject)
	writer, err := blob.Writer()
	if err != nil {
		return err
	}
	if _, err = writer.Write(contents); err != nil {
		_ = writer.Close()
		return err
	}
	if err = writer.Close(); err != nil {
		return err
	}
	blobHash, err := repository.Storer.SetEncodedObject(blob)
	if err != nil {
		return err
	}

	entries := append([]object.TreeEntry(nil), tree.Entries...)
	found := false
	for index := range entries {
		if entries[index].Name == "control.json" {
			entries[index].Mode = filemode.Regular
			entries[index].Hash = blobHash
			found = true
			break
		}
	}
	if !found {
		entries = append(entries, object.TreeEntry{
			Name: "control.json",
			Mode: filemode.Regular,
			Hash: blobHash,
		})
	}
	sort.Sort(object.TreeEntrySorter(entries))
	updatedTree := &object.Tree{Entries: entries}
	encodedTree := &plumbing.MemoryObject{}
	if err = updatedTree.Encode(encodedTree); err != nil {
		return err
	}
	treeHash, err := repository.Storer.SetEncodedObject(encodedTree)
	if err != nil {
		return err
	}

	if author == "" {
		author = "GitOne"
	}
	signature := object.Signature{
		Name:  author,
		Email: author + "@localhost",
		When:  time.Now().UTC(),
	}
	commit := &object.Commit{
		Author:       signature,
		Committer:    signature,
		Message:      "Update group control\n",
		TreeHash:     treeHash,
		ParentHashes: []plumbing.Hash{parent.Hash},
	}
	encodedCommit := &plumbing.MemoryObject{}
	if err = commit.Encode(encodedCommit); err != nil {
		return err
	}
	commitHash, err := repository.Storer.SetEncodedObject(encodedCommit)
	if err != nil {
		return err
	}
	return repository.Storer.CheckAndSetReference(
		plumbing.NewHashReference(plumbing.NewBranchReferenceName("main"), commitHash),
		head,
	)
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
	buildp, err := s.BuildPath(r)
	if err != nil {
		return err
	}
	trash, err := repopath.SafeJoin(s.Root, ".trash", time.Now().UTC().Format("20060102T150405.000000000"), r.Group())
	if err != nil {
		return err
	}
	if err = os.MkdirAll(trash, 0o750); err != nil {
		return err
	}
	if err = os.Rename(gitp, filepath.Join(trash, r.Name+".git")); err != nil {
		return err
	}
	if _, statErr := os.Stat(lfsp); statErr == nil {
		_ = os.Rename(lfsp, filepath.Join(trash, r.Name+".lfs"))
	}
	if _, statErr := os.Stat(buildp); statErr == nil {
		_ = os.Rename(buildp, filepath.Join(trash, r.Name+".build"))
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
	buildp, err := s.BuildPath(r)
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
	dstBuild, err := repopath.SafeJoin(s.Root, append(r.Groups, newName+".build")...)
	if err != nil {
		return err
	}
	if err = os.Rename(gitp, dstGit); err != nil {
		return err
	}
	lfsMoved := false
	if _, statErr := os.Stat(lfsp); statErr == nil {
		if err = os.Rename(lfsp, dstLFS); err != nil {
			_ = os.Rename(dstGit, gitp)
			return err
		}
		lfsMoved = true
	}
	if _, statErr := os.Stat(buildp); statErr == nil {
		if err = os.Rename(buildp, dstBuild); err != nil {
			if lfsMoved {
				_ = os.Rename(dstLFS, lfsp)
			}
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
	if err = os.MkdirAll(trash, 0o750); err != nil {
		return err
	}
	return os.Rename(gp, filepath.Join(trash, strings.ReplaceAll(group, "/", "__")))
}

func (s Store) RenameGroup(group, newPath string) error {
	if newPath == group || strings.HasPrefix(newPath, group+"/") {
		return errors.New("cannot move a group into itself")
	}
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
	root, err := repopath.SafeJoin(s.Root)
	if err != nil {
		return err
	}
	parent := filepath.Dir(dst)
	if parent != root {
		if _, err = os.Stat(filepath.Join(parent, "control.git")); err != nil {
			return errors.New("destination parent group does not exist")
		}
	}
	if err = os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	return os.Rename(src, dst)
}
