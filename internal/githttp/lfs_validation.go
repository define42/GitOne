package githttp

import (
	"fmt"
	"io"
	"path"

	"github.com/define42/GitOne/internal/lfs"
	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/storage"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
)

func validateLFSPointerUpdates(
	repository *git.Repository,
	store storage.Store,
	repositoryPath repopath.Repository,
	commands []*packp.Command,
) error {
	ignored, err := referencedCommits(repository)
	if err != nil {
		return fmt.Errorf("inspect current references: %w", err)
	}

	seenCommits := make(map[plumbing.Hash]bool)
	seenTrees := make(map[plumbing.Hash]bool)
	seenBlobs := make(map[plumbing.Hash]bool)
	for _, command := range commands {
		if command.New == plumbing.ZeroHash {
			continue
		}
		commit, err := peelCommit(repository, command.New)
		if err != nil {
			return fmt.Errorf("load new object for %s: %w", command.Name, err)
		}
		if commit == nil {
			if command.Name.IsBranch() {
				return fmt.Errorf("%s must point to a commit", command.Name)
			}
			continue
		}

		commits := object.NewCommitPreorderIter(commit, seenCommits, ignored)
		err = commits.ForEach(func(candidate *object.Commit) error {
			seenCommits[candidate.Hash] = true
			return validateCommitLFSPointers(
				repository,
				candidate,
				store,
				repositoryPath,
				seenTrees,
				seenBlobs,
			)
		})
		commits.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func referencedCommits(repository *git.Repository) ([]plumbing.Hash, error) {
	references, err := repository.References()
	if err != nil {
		return nil, err
	}
	defer references.Close()

	seenReferences := make(map[plumbing.Hash]bool)
	seenCommits := make(map[plumbing.Hash]bool)
	var commits []plumbing.Hash
	err = references.ForEach(func(reference *plumbing.Reference) error {
		if reference.Type() != plumbing.HashReference || seenReferences[reference.Hash()] {
			return nil
		}
		seenReferences[reference.Hash()] = true
		commit, peelErr := peelCommit(repository, reference.Hash())
		if peelErr != nil {
			return fmt.Errorf("resolve %s: %w", reference.Name(), peelErr)
		}
		if commit == nil || seenCommits[commit.Hash] {
			return nil
		}
		seenCommits[commit.Hash] = true
		commits = append(commits, commit.Hash)
		return nil
	})
	return commits, err
}

func peelCommit(repository *git.Repository, hash plumbing.Hash) (*object.Commit, error) {
	seen := make(map[plumbing.Hash]bool)
	for {
		if seen[hash] {
			return nil, fmt.Errorf("tag cycle at %s", hash)
		}
		seen[hash] = true
		current, err := repository.Object(plumbing.AnyObject, hash)
		if err != nil {
			return nil, err
		}
		switch typed := current.(type) {
		case *object.Commit:
			return typed, nil
		case *object.Tag:
			hash = typed.Target
		default:
			return nil, nil
		}
	}
}

func validateCommitLFSPointers(
	repository *git.Repository,
	commit *object.Commit,
	store storage.Store,
	repositoryPath repopath.Repository,
	seenTrees map[plumbing.Hash]bool,
	seenBlobs map[plumbing.Hash]bool,
) error {
	tree, err := commit.Tree()
	if err != nil {
		return fmt.Errorf("load tree for commit %s: %w", commit.Hash, err)
	}
	if seenTrees[tree.Hash] {
		return nil
	}
	return validateTreeLFSPointers(
		repository,
		tree,
		"",
		commit.Hash,
		store,
		repositoryPath,
		seenTrees,
		seenBlobs,
	)
}

func validateTreeLFSPointers(
	repository *git.Repository,
	tree *object.Tree,
	prefix string,
	commit plumbing.Hash,
	store storage.Store,
	repositoryPath repopath.Repository,
	seenTrees map[plumbing.Hash]bool,
	seenBlobs map[plumbing.Hash]bool,
) error {
	if seenTrees[tree.Hash] {
		return nil
	}
	seenTrees[tree.Hash] = true
	for _, entry := range tree.Entries {
		name := path.Join(prefix, entry.Name)
		switch {
		case entry.Mode == filemode.Dir:
			subtree, err := repository.TreeObject(entry.Hash)
			if err != nil {
				return fmt.Errorf("load tree %q in commit %s: %w", name, commit, err)
			}
			if err = validateTreeLFSPointers(
				repository,
				subtree,
				name,
				commit,
				store,
				repositoryPath,
				seenTrees,
				seenBlobs,
			); err != nil {
				return err
			}
			continue
		case entry.Mode == filemode.Submodule:
			continue
		case !entry.Mode.IsFile():
			return fmt.Errorf("invalid mode %s for %q in commit %s", entry.Mode, name, commit)
		}
		if seenBlobs[entry.Hash] {
			continue
		}
		seenBlobs[entry.Hash] = true
		blob, err := repository.BlobObject(entry.Hash)
		if err != nil {
			return fmt.Errorf("load %q in commit %s: %w", name, commit, err)
		}
		if blob.Size > lfs.MaxPointerSize {
			continue
		}
		reader, err := blob.Reader()
		if err != nil {
			return fmt.Errorf("open %q in commit %s: %w", name, commit, err)
		}
		content, readErr := io.ReadAll(io.LimitReader(reader, lfs.MaxPointerSize+1))
		closeErr := reader.Close()
		if readErr != nil {
			return fmt.Errorf("read %q in commit %s: %w", name, commit, readErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close %q in commit %s: %w", name, commit, closeErr)
		}
		pointer, ok := lfs.ParsePointer(content)
		if !ok {
			continue
		}
		if err = lfs.VerifyObject(store, repositoryPath, pointer.OID, pointer.Size); err != nil {
			return fmt.Errorf(
				"invalid LFS pointer %q in commit %s: %w",
				name,
				commit,
				err,
			)
		}
	}
	return nil
}
