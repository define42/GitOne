package runner

import (
	"errors"

	"github.com/define42/GitOne/internal/gitformat"
	"github.com/define42/GitOne/internal/lockmgr"
	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/storage"
	git "github.com/go-git/go-git/v6"
)

func acquireBuildOperationLock(
	root string,
	repository repopath.Repository,
	jobIDs ...string,
) (func() error, error) {
	requests := lockmgr.RepositoryRequests(
		root,
		[]repopath.Repository{repository},
		lockmgr.Shared,
	)
	for _, id := range jobIDs {
		requests = append(requests, lockmgr.JobRequest(root, repository, id))
	}
	release, err := lockmgr.Process.Acquire(requests...)
	if err != nil {
		return nil, err
	}
	return func() error {
		release()
		return nil
	}, nil
}

func openRepositoryForBuild(
	store storage.Store,
	repositoryPath repopath.Repository,
) (*git.Repository, error) {
	path, err := store.GitPath(repositoryPath)
	if err != nil {
		return nil, err
	}
	return gitformat.Open(path)
}

func acquireRepositoryBuildLock(
	store storage.Store,
	repositoryPath repopath.Repository,
	jobIDs ...string,
) (func() error, *git.Repository, error) {
	release, err := acquireBuildOperationLock(store.Root, repositoryPath, jobIDs...)
	if err != nil {
		return nil, nil, err
	}
	repository, err := openRepositoryForBuild(store, repositoryPath)
	if err != nil {
		return nil, nil, errors.Join(err, release())
	}
	return release, repository, nil
}
