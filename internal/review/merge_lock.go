package review

import (
	"github.com/define42/GitOne/internal/lockmgr"
	"github.com/define42/GitOne/internal/repopath"
)

// AcquireMergeLock serializes merge transactions for a repository within the
// GitOne web-server process.
func (s *Store) AcquireMergeLock(repository repopath.Repository) (func() error, error) {
	if err := validateRepository(repository); err != nil {
		return nil, err
	}
	release, err := lockmgr.Process.Acquire(lockmgr.MergeRequest(s.Root, repository))
	if err != nil {
		return nil, err
	}
	return func() error {
		release()
		return nil
	}, nil
}

// AcquireOperationLock is retained as a compatibility barrier. Production
// mutation paths use resource-specific locks; this barrier waits for all of
// them through the catalog lock.
func (s *Store) AcquireOperationLock() (func() error, error) {
	release, err := lockmgr.Process.Acquire(
		lockmgr.CatalogRequest(s.Root, lockmgr.Exclusive),
	)
	if err != nil {
		return nil, err
	}
	return func() error {
		release()
		return nil
	}, nil
}

func (s *Store) lockRepositories(
	repositories ...repopath.Repository,
) (func() error, error) {
	release, err := lockmgr.Process.Acquire(
		lockmgr.ReviewRepositoryRequests(s.Root, repositories)...,
	)
	if err != nil {
		return nil, err
	}
	return func() error {
		release()
		return nil
	}, nil
}

func (s *Store) lockGroups(groups ...string) (func() error, error) {
	release, err := lockmgr.Process.Acquire(lockmgr.ReviewGroupRequests(s.Root, groups)...)
	if err != nil {
		return nil, err
	}
	return func() error {
		release()
		return nil
	}, nil
}
