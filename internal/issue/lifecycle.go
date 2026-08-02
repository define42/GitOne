package issue

import (
	"errors"

	"github.com/define42/GitOne/internal/lockmgr"
	"github.com/define42/GitOne/internal/repopath"
)

// WithRepositoryLocks runs an action while the issue records of the named
// repositories are locked. Issue locks sort after review locks, so callers may
// acquire them inside a review-locked section but never the reverse.
func (s *Store) WithRepositoryLocks(
	repositories []repopath.Repository,
	action func() error,
) error {
	if action == nil {
		return errors.New("issue repository action is required")
	}
	unlock, err := s.lockRepositories(repositories...)
	if err != nil {
		return err
	}
	defer func() {
		_ = unlock()
	}()
	return action()
}

// WithGroupLocks runs an action while the issue records below the named groups
// are locked.
func (s *Store) WithGroupLocks(groups []string, action func() error) error {
	if action == nil {
		return errors.New("issue group action is required")
	}
	unlock, err := s.lockGroups(groups...)
	if err != nil {
		return err
	}
	defer func() {
		_ = unlock()
	}()
	return action()
}

// MoveRepositoryLocked moves repository state and relocates the repository's
// issue records as one transaction. The caller holds the repository operations
// lock; move is rolled back with rollback when the relocation fails.
func (s *Store) MoveRepositoryLocked(
	source repopath.Repository,
	destination repopath.Repository,
	move func() error,
	rollback func() error,
) error {
	if move == nil || rollback == nil {
		return errors.New("repository move and rollback actions are required")
	}
	return s.WithRepositoryLocks(
		[]repopath.Repository{source, destination},
		func() error {
			if moveErr := move(); moveErr != nil {
				if rollbackErr := rollback(); rollbackErr != nil {
					return errors.Join(moveErr, rollbackErr)
				}
				return moveErr
			}
			if relocateErr := s.relocate(source, destination); relocateErr != nil {
				if rollbackErr := rollback(); rollbackErr != nil {
					return errors.Join(relocateErr, rollbackErr)
				}
				return relocateErr
			}
			return nil
		},
	)
}

// MoveGroupLocked moves group state and rewrites the embedded issue records as
// one transaction while its caller holds the repository operations lock.
func (s *Store) MoveGroupLocked(
	sourceGroup string,
	destinationGroup string,
	move func() error,
	rollback func() error,
) error {
	if move == nil || rollback == nil {
		return errors.New("group move and rollback actions are required")
	}
	return s.WithGroupLocks([]string{sourceGroup, destinationGroup}, func() error {
		if moveErr := move(); moveErr != nil {
			return moveErr
		}
		if rewriteErr := s.rewriteGroup(sourceGroup, destinationGroup); rewriteErr != nil {
			if rollbackErr := rollback(); rollbackErr != nil {
				return errors.Join(rewriteErr, rollbackErr)
			}
			return rewriteErr
		}
		return nil
	})
}

func (s *Store) lockRepositories(
	repositories ...repopath.Repository,
) (func() error, error) {
	release, err := lockmgr.Process.Acquire(
		lockmgr.IssueRepositoryRequests(s.Root, repositories)...,
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
	release, err := lockmgr.Process.Acquire(lockmgr.IssueGroupRequests(s.Root, groups)...)
	if err != nil {
		return nil, err
	}
	return func() error {
		release()
		return nil
	}, nil
}
