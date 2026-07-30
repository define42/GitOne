package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/define42/GitOne/internal/control"
	"github.com/define42/GitOne/internal/lockmgr"
	"github.com/define42/GitOne/internal/repoconfig"
	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/review"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	gittransport "github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"gopkg.in/yaml.v3"
)

type Store struct{ Root string }

type CreateRepositoryOptions struct {
	InitializeReadme bool
	Author           string
	Description      string
}

type ImportRepositoryOptions struct {
	URL      string
	Username string
	Password string
}

type RemoteImportError struct {
	Err error
}

func (e *RemoteImportError) Error() string {
	switch {
	case errors.Is(e.Err, context.Canceled):
		return "remote repository import was canceled"
	case errors.Is(e.Err, context.DeadlineExceeded):
		return "remote repository import timed out"
	case errors.Is(e.Err, gittransport.ErrAuthenticationRequired):
		return "remote repository requires authentication"
	case errors.Is(e.Err, gittransport.ErrAuthorizationFailed):
		return "remote repository rejected the supplied credentials"
	case errors.Is(e.Err, gittransport.ErrRepositoryNotFound):
		return "remote repository was not found"
	case errors.Is(e.Err, gittransport.ErrEmptyRemoteRepository):
		return "remote repository is empty"
	default:
		return "could not import the remote repository"
	}
}

func (e *RemoteImportError) Unwrap() error {
	return e.Err
}

type GroupInfo struct {
	Path         string
	Repositories []string
}

func pathEntryExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
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

func (s Store) ReviewPath(r repopath.Repository) (string, error) {
	return repopath.SafeJoin(s.Root, append(r.Groups, r.Name+".reviews")...)
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
				strings.HasSuffix(entry.Name(), ".build") ||
				strings.HasSuffix(entry.Name(), ".reviews") {
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
	releaseOperation, err := lockmgr.Process.Acquire(
		lockmgr.RepositoryRequests(s.Root, []repopath.Repository{r}, lockmgr.Exclusive)...,
	)
	if err != nil {
		return err
	}
	defer releaseOperation()
	return s.CreateRepositoryLocked(r, options)
}

// CreateRepositoryLocked creates a repository while its caller holds the
// repository operations lock.
func (s Store) CreateRepositoryLocked(
	r repopath.Repository,
	options CreateRepositoryOptions,
) error {
	if r.Name == "control" {
		return errors.New("reserved repository name")
	}
	return review.NewStore(s.Root).WithRepositoryLocks([]repopath.Repository{r}, func() error {
		return s.createRepository(r, options)
	})
}

func (s Store) ImportRepository(
	ctx context.Context,
	r repopath.Repository,
	options ImportRepositoryOptions,
) error {
	return s.ImportRepositoryValidated(ctx, r, options, nil)
}

// ImportRepositoryValidated stages the remote clone without holding a
// repository lock. Once staging is complete, it locks and revalidates the
// destination, calls validate, and atomically publishes the repository.
func (s Store) ImportRepositoryValidated(
	ctx context.Context,
	r repopath.Repository,
	options ImportRepositoryOptions,
	validate func() error,
) error {
	if r.Name == "control" {
		return errors.New("reserved repository name")
	}
	if err := s.checkImportDestination(r); err != nil {
		return err
	}
	staged, err := s.stageRemoteRepository(ctx, options)
	if err != nil {
		return err
	}
	defer func() {
		_ = os.RemoveAll(staged.root)
	}()

	releaseOperation, err := lockmgr.Process.Acquire(
		lockmgr.RepositoryRequests(s.Root, []repopath.Repository{r}, lockmgr.Exclusive)...,
	)
	if err != nil {
		return err
	}
	defer releaseOperation()
	if validate != nil {
		if err = validate(); err != nil {
			return err
		}
	}
	return review.NewStore(s.Root).WithRepositoryLocks([]repopath.Repository{r}, func() error {
		destination, prepareErr := s.prepareImportDestination(r)
		if prepareErr != nil {
			return prepareErr
		}
		return adoptStagedRemoteRepository(
			staged,
			destination.gitPath,
			destination.lfsPath,
		)
	})
}

// ImportRepositoryLocked mirrors a remote HTTP(S) repository into a bare
// repository while its caller holds the repository operations lock.
func (s Store) ImportRepositoryLocked(
	ctx context.Context,
	r repopath.Repository,
	options ImportRepositoryOptions,
) error {
	if r.Name == "control" {
		return errors.New("reserved repository name")
	}
	return review.NewStore(s.Root).WithRepositoryLocks([]repopath.Repository{r}, func() error {
		return s.importRepository(ctx, r, options)
	})
}

func (s Store) importRepository(
	ctx context.Context,
	r repopath.Repository,
	options ImportRepositoryOptions,
) error {
	destination, err := s.prepareImportDestination(r)
	if err != nil {
		return err
	}
	staged, err := s.stageRemoteRepository(ctx, options)
	if err != nil {
		return err
	}
	defer func() {
		_ = os.RemoveAll(staged.root)
	}()

	return adoptStagedRemoteRepository(
		staged,
		destination.gitPath,
		destination.lfsPath,
	)
}

type stagedRemoteRepository struct {
	root    string
	gitPath string
	lfsPath string
}

func (s Store) stageRemoteRepository(
	ctx context.Context,
	options ImportRepositoryOptions,
) (stagedRemoteRepository, error) {
	temporaryRoot, err := s.newImportStagingDirectory()
	if err != nil {
		return stagedRemoteRepository{}, err
	}
	staged := stagedRemoteRepository{
		root:    temporaryRoot,
		gitPath: filepath.Join(temporaryRoot, "repository.git"),
		lfsPath: filepath.Join(temporaryRoot, "repository.lfs"),
	}
	cloneURL, usesImportTransport := importTransportURL(options.URL)
	cloneOptions := &git.CloneOptions{
		URL:    cloneURL,
		Mirror: true,
	}
	if options.Username != "" {
		cloneOptions.Auth = &githttp.BasicAuth{
			Username: options.Username,
			Password: options.Password,
		}
	}
	cloned, err := git.PlainCloneContext(ctx, staged.gitPath, true, cloneOptions)
	if err != nil {
		_ = os.RemoveAll(temporaryRoot)
		return stagedRemoteRepository{}, &RemoteImportError{Err: err}
	}
	if usesImportTransport {
		configuration, configErr := cloned.Config()
		if configErr != nil {
			_ = os.RemoveAll(temporaryRoot)
			return stagedRemoteRepository{}, configErr
		}
		origin := configuration.Remotes[git.DefaultRemoteName]
		if origin == nil {
			_ = os.RemoveAll(temporaryRoot)
			return stagedRemoteRepository{}, errors.New("imported repository has no origin remote")
		}
		origin.URLs = []string{options.URL}
		if configErr = cloned.Storer.SetConfig(configuration); configErr != nil {
			_ = os.RemoveAll(temporaryRoot)
			return stagedRemoteRepository{}, configErr
		}
	}
	if err = importRemoteLFS(ctx, cloned, options, staged.lfsPath); err != nil {
		_ = os.RemoveAll(temporaryRoot)
		return stagedRemoteRepository{}, &RemoteImportError{Err: err}
	}
	return staged, nil
}

func adoptStagedRemoteRepository(
	staged stagedRemoteRepository,
	gitPath string,
	lfsPath string,
) error {
	if err := os.Chmod(staged.gitPath, 0o750); err != nil {
		return err
	}
	if err := os.Chmod(staged.lfsPath, 0o750); err != nil {
		return err
	}
	if err := os.Rename(staged.gitPath, gitPath); err != nil {
		return err
	}
	if err := os.Rename(staged.lfsPath, lfsPath); err != nil {
		_ = os.RemoveAll(gitPath)
		_ = os.RemoveAll(lfsPath)
		return err
	}
	return nil
}

func (s Store) checkImportDestination(r repopath.Repository) error {
	release, err := lockmgr.Process.Acquire(
		lockmgr.RepositoryRequests(s.Root, []repopath.Repository{r}, lockmgr.Exclusive)...,
	)
	if err != nil {
		return err
	}
	defer release()
	_, err = s.prepareImportDestination(r)
	return err
}

func (s Store) createRepository(r repopath.Repository, options CreateRepositoryOptions) error {
	gp, e := s.GroupPath(r.Group())
	if e != nil {
		return e
	}
	if _, e = os.Stat(filepath.Join(gp, "control.git")); e != nil {
		return errors.New("group does not exist")
	}
	gitp, e := s.GitPath(r)
	if e != nil {
		return e
	}
	lfsp, e := s.LFSPath(r)
	if e != nil {
		return e
	}
	buildp, e := s.BuildPath(r)
	if e != nil {
		return e
	}
	reviewp, e := s.ReviewPath(r)
	if e != nil {
		return e
	}
	for _, existing := range []string{gitp, lfsp, buildp, reviewp} {
		exists, statErr := pathEntryExists(existing)
		if statErr != nil {
			return statErr
		}
		if exists {
			return errors.New("repository data already exists")
		}
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
		metadata, marshalErr := yaml.Marshal(repoconfig.Config{Description: options.Description})
		if marshalErr != nil {
			return marshalErr
		}
		if err = os.WriteFile(
			filepath.Join(temporary, repoconfig.FileName),
			metadata,
			0o640,
		); err != nil {
			return err
		}
		if _, err = worktree.Add(repoconfig.FileName); err != nil {
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
	releaseOperation, err := lockmgr.Process.Acquire(
		lockmgr.GroupRequests(s.Root, []string{group}, lockmgr.Exclusive)...,
	)
	if err != nil {
		return err
	}
	defer releaseOperation()
	return s.CreateGroupLocked(group, owner, description)
}

// CreateGroupLocked creates a group while its caller holds the repository
// operations lock.
func (s Store) CreateGroupLocked(group, owner, description string) error {
	return review.NewStore(s.Root).WithGroupLocks([]string{group}, func() error {
		return s.createGroup(group, owner, owner, description)
	})
}

// CreateInheritedGroupLocked creates an inherited subgroup without assigning
// its creator a direct role while its caller holds the repository operations
// lock.
func (s Store) CreateInheritedGroupLocked(group, author, description string) error {
	return review.NewStore(s.Root).WithGroupLocks([]string{group}, func() error {
		return s.createGroup(group, "", author, description)
	})
}

func (s Store) createGroup(group, owner, author, description string) error {
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
	members := map[string]control.Role{}
	if owner != "" {
		members[owner] = control.RoleOwner
	}
	doc := control.Document{
		Version:     control.CurrentVersion,
		Group:       group,
		Description: description,
		Inherit:     true,
		Visibility:  "private",
		LFS:         control.LFSPolicy{Enabled: true},
		Members:     members,
		Tokens:      []control.Token{},
	}
	b, _ := json.MarshalIndent(doc, "", "  ")
	b = append(b, '\n')
	if e = os.WriteFile(filepath.Join(tmp, "control.json"), b, 0o640); e != nil {
		return e
	}
	if _, e = wt.Add("control.json"); e != nil {
		return e
	}
	commit, e := wt.Commit("Initialize group control", &git.CommitOptions{Author: &object.Signature{Name: author, Email: author + "@localhost", When: time.Now().UTC()}})
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
	releaseOperation, err := lockmgr.Process.Acquire(
		lockmgr.GroupRequests(s.Root, []string{group}, lockmgr.Exclusive)...,
	)
	if err != nil {
		return err
	}
	defer releaseOperation()
	return s.UpdateGroupControlLocked(group, document, author)
}

// UpdateGroupControlLocked commits control state while its caller holds the
// repository operations lock.
func (s Store) UpdateGroupControlLocked(
	group string,
	document control.Document,
	author string,
) error {
	if err := control.Validate(group, document); err != nil {
		return err
	}
	return s.updateGroupControl(group, document, author)
}

func (s Store) updateGroupControl(group string, document control.Document, author string) error {
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
	releaseOperation, err := lockmgr.Process.Acquire(
		lockmgr.RepositoryRequests(s.Root, []repopath.Repository{r}, lockmgr.Exclusive)...,
	)
	if err != nil {
		return err
	}
	defer releaseOperation()
	return s.DeleteRepositoryLocked(r)
}

// DeleteRepositoryLocked deletes repository data while its caller holds the
// repository operations lock.
func (s Store) DeleteRepositoryLocked(r repopath.Repository) error {
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
	reviewp, err := s.ReviewPath(r)
	if err != nil {
		return err
	}
	trash, err := repopath.SafeJoin(s.Root, ".trash", time.Now().UTC().Format("20060102T150405.000000000"), r.Group())
	if err != nil {
		return err
	}
	reviews := review.NewStore(s.Root)
	return reviews.WithRepositoryLocks([]repopath.Repository{r}, func() error {
		if err = os.MkdirAll(trash, 0o750); err != nil {
			return err
		}
		type repositoryMove struct {
			source      string
			destination string
		}
		moves := []repositoryMove{{
			source:      gitp,
			destination: filepath.Join(trash, r.Name+".git"),
		}}
		for _, sidecar := range []repositoryMove{
			{source: lfsp, destination: filepath.Join(trash, r.Name+".lfs")},
			{source: buildp, destination: filepath.Join(trash, r.Name+".build")},
			{source: reviewp, destination: filepath.Join(trash, r.Name+".reviews")},
		} {
			exists, statErr := pathEntryExists(sidecar.source)
			if statErr != nil {
				return statErr
			}
			if exists {
				moves = append(moves, sidecar)
			}
		}
		for _, move := range moves {
			exists, statErr := pathEntryExists(move.destination)
			if statErr != nil {
				return statErr
			}
			if exists {
				return fmt.Errorf("trash destination already exists: %s", move.destination)
			}
		}
		for index, move := range moves {
			if err = os.Rename(move.source, move.destination); err == nil {
				continue
			}
			var rollbackErr error
			for rollback := index - 1; rollback >= 0; rollback-- {
				candidate := moves[rollback]
				if restoreErr := os.Rename(candidate.destination, candidate.source); restoreErr != nil {
					rollbackErr = errors.Join(rollbackErr, restoreErr)
				}
			}
			if rollbackErr != nil {
				return errors.Join(err, fmt.Errorf("restore repository after failed delete: %w", rollbackErr))
			}
			return err
		}
		return nil
	})
}

func (s Store) RenameRepository(r repopath.Repository, newName string) error {
	if newName == "" || newName == "control" || filepath.Base(newName) != newName {
		return errors.New("invalid repository name")
	}
	renamed := repopath.Repository{
		Groups: append([]string(nil), r.Groups...),
		Name:   newName,
	}
	releaseOperation, err := lockmgr.Process.Acquire(
		lockmgr.RepositoryRequests(
			s.Root,
			[]repopath.Repository{r, renamed},
			lockmgr.Exclusive,
		)...,
	)
	if err != nil {
		return err
	}
	defer releaseOperation()
	return s.RenameRepositoryLocked(r, newName)
}

// RenameRepositoryLocked renames repository data while its caller holds the
// repository operations lock.
func (s Store) RenameRepositoryLocked(r repopath.Repository, newName string) error {
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
	reviewp, err := s.ReviewPath(r)
	if err != nil {
		return err
	}
	renamed := repopath.Repository{
		Groups: append([]string(nil), r.Groups...),
		Name:   newName,
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
	dstReview, err := s.ReviewPath(renamed)
	if err != nil {
		return err
	}
	reviews := review.NewStore(s.Root)
	gitMoved := false
	lfsMoved := false
	buildMoved := false
	return reviews.MoveRepositoryLocked(
		r,
		renamed,
		func() error {
			for _, destination := range []string{dstGit, dstLFS, dstBuild, dstReview} {
				exists, statErr := pathEntryExists(destination)
				if statErr != nil {
					return statErr
				}
				if exists {
					return fmt.Errorf("destination repository data already exists: %s", destination)
				}
			}
			lfsExists, existsErr := pathEntryExists(lfsp)
			if existsErr != nil {
				return existsErr
			}
			buildExists, existsErr := pathEntryExists(buildp)
			if existsErr != nil {
				return existsErr
			}
			reviewExists, existsErr := pathEntryExists(reviewp)
			if existsErr != nil {
				return existsErr
			}
			if reviewExists {
				info, statErr := os.Lstat(reviewp)
				if statErr != nil {
					return statErr
				}
				if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
					return errors.New("review store is not a directory")
				}
			}
			if renameErr := os.Rename(gitp, dstGit); renameErr != nil {
				return renameErr
			}
			gitMoved = true
			if lfsExists {
				if renameErr := os.Rename(lfsp, dstLFS); renameErr != nil {
					return renameErr
				}
				lfsMoved = true
			}
			if buildExists {
				if renameErr := os.Rename(buildp, dstBuild); renameErr != nil {
					return renameErr
				}
				buildMoved = true
			}
			return nil
		},
		func() error {
			var rollbackErr error
			if buildMoved {
				if restoreErr := os.Rename(dstBuild, buildp); restoreErr != nil {
					rollbackErr = errors.Join(rollbackErr, restoreErr)
				}
			}
			if lfsMoved {
				if restoreErr := os.Rename(dstLFS, lfsp); restoreErr != nil {
					rollbackErr = errors.Join(rollbackErr, restoreErr)
				}
			}
			if gitMoved {
				if restoreErr := os.Rename(dstGit, gitp); restoreErr != nil {
					rollbackErr = errors.Join(rollbackErr, restoreErr)
				}
			}
			return rollbackErr
		},
	)
}

func (s Store) DeleteGroup(group string) error {
	releaseOperation, err := lockmgr.Process.Acquire(
		lockmgr.GroupRequests(s.Root, []string{group}, lockmgr.Exclusive)...,
	)
	if err != nil {
		return err
	}
	defer releaseOperation()
	return s.DeleteGroupLocked(group)
}

// DeleteGroupLocked deletes a group while its caller holds the repository
// operations lock.
func (s Store) DeleteGroupLocked(group string) error {
	return review.NewStore(s.Root).WithGroupLocks([]string{group}, func() error {
		return s.deleteGroup(group)
	})
}

func (s Store) deleteGroup(group string) error {
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
	releaseOperation, err := lockmgr.Process.Acquire(
		lockmgr.GroupRequests(
			s.Root,
			[]string{group, newPath},
			lockmgr.Exclusive,
		)...,
	)
	if err != nil {
		return err
	}
	defer releaseOperation()
	return s.RenameGroupLocked(group, newPath)
}

// RenameGroupLocked renames a group while its caller holds the repository
// operations lock.
func (s Store) RenameGroupLocked(group, newPath string) error {
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
	root, err := repopath.SafeJoin(s.Root)
	if err != nil {
		return err
	}
	reviews := review.NewStore(s.Root)
	return reviews.MoveGroupLocked(
		group,
		newPath,
		func() error {
			destinationExists, existsErr := pathEntryExists(dst)
			if existsErr != nil {
				return existsErr
			}
			if destinationExists {
				return errors.New("destination group exists")
			}
			parent := filepath.Dir(dst)
			if parent != root {
				if _, parentErr := os.Stat(
					filepath.Join(parent, "control.git"),
				); parentErr != nil {
					return errors.New("destination parent group does not exist")
				}
			}
			if mkdirErr := os.MkdirAll(parent, 0o750); mkdirErr != nil {
				return mkdirErr
			}
			return os.Rename(src, dst)
		},
		func() error { return os.Rename(dst, src) },
	)
}
