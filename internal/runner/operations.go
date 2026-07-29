package runner

import (
	"errors"

	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/review"
	"github.com/define42/GitOne/internal/storage"
	git "github.com/go-git/go-git/v5"
)

func acquireBuildOperationLock(root string) (func() error, error) {
	return review.NewStore(root).AcquireOperationLock()
}

func openRepositoryForBuild(
	store storage.Store,
	repositoryPath repopath.Repository,
) (*git.Repository, error) {
	path, err := store.GitPath(repositoryPath)
	if err != nil {
		return nil, err
	}
	return git.PlainOpen(path)
}

func acquireRepositoryBuildLock(
	store storage.Store,
	repositoryPath repopath.Repository,
) (func() error, *git.Repository, error) {
	release, err := acquireBuildOperationLock(store.Root)
	if err != nil {
		return nil, nil, err
	}
	repository, err := openRepositoryForBuild(store, repositoryPath)
	if err != nil {
		return nil, nil, errors.Join(err, release())
	}
	return release, repository, nil
}
