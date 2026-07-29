package review

import (
	"errors"
	"fmt"

	"github.com/define42/GitOne/internal/lockmgr"
	"github.com/define42/GitOne/internal/repopath"
)

// WithLifecycleLock coordinates filesystem moves with review store mutations.
// The action must not call another Store method for the same root.
func (s *Store) WithLifecycleLock(action func() error) error {
	if action == nil {
		return errors.New("review lifecycle action is required")
	}
	unlock, err := s.lockLifecycle()
	if err != nil {
		return err
	}
	defer func() {
		_ = unlock()
	}()
	return action()
}

// WithLifecycleLockHeld coordinates review store mutations while its caller
// already holds the repository operations lock.
func (s *Store) WithLifecycleLockHeld(action func() error) error {
	if action == nil {
		return errors.New("review lifecycle action is required")
	}
	release, err := lockmgr.Process.Acquire(
		lockmgr.ReviewCatalogRequest(s.Root, lockmgr.Exclusive),
	)
	if err != nil {
		return err
	}
	defer func() {
		release()
	}()
	return action()
}

func (s *Store) WithRepositoryLocks(
	repositories []repopath.Repository,
	action func() error,
) error {
	if action == nil {
		return errors.New("review repository action is required")
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

func (s *Store) WithGroupLocks(groups []string, action func() error) error {
	if action == nil {
		return errors.New("review group action is required")
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

// MoveRepository coordinates non-review sidecar moves with relocation of the
// repository's persisted review records.
func (s *Store) MoveRepository(
	source repopath.Repository,
	destination repopath.Repository,
	move func() error,
	rollback func() error,
) error {
	if move == nil || rollback == nil {
		return errors.New("repository move and rollback actions are required")
	}
	releaseOperation, err := lockmgr.Process.Acquire(
		lockmgr.RepositoryRequests(
			s.Root,
			[]repopath.Repository{source, destination},
			lockmgr.Exclusive,
		)...,
	)
	if err != nil {
		return err
	}
	defer func() {
		releaseOperation()
	}()
	return s.MoveRepositoryLocked(source, destination, move, rollback)
}

// MoveRepositoryLocked moves repository state while its caller holds the
// repository operations lock.
func (s *Store) MoveRepositoryLocked(
	source repopath.Repository,
	destination repopath.Repository,
	move func() error,
	rollback func() error,
) error {
	if move == nil || rollback == nil {
		return errors.New("repository move and rollback actions are required")
	}
	unlock, err := s.lockRepositories(source, destination)
	if err != nil {
		return err
	}
	defer func() {
		_ = unlock()
	}()
	return s.moveRepository(source, destination, move, rollback)
}

func (s *Store) moveRepository(
	source repopath.Repository,
	destination repopath.Repository,
	move func() error,
	rollback func() error,
) error {
	if moveErr := move(); moveErr != nil {
		if rollbackErr := rollback(); rollbackErr != nil {
			return errors.Join(moveErr, fmt.Errorf("restore repository data: %w", rollbackErr))
		}
		return moveErr
	}
	if relocateErr := s.relocate(source, destination); relocateErr != nil {
		if rollbackErr := rollback(); rollbackErr != nil {
			return errors.Join(
				relocateErr,
				fmt.Errorf("restore repository data: %w", rollbackErr),
			)
		}
		return relocateErr
	}
	return nil
}

// MoveGroup coordinates a group directory move and its embedded review record
// rewrites as one lifecycle transaction.
func (s *Store) MoveGroup(
	sourceGroup string,
	destinationGroup string,
	move func() error,
	rollback func() error,
) error {
	if move == nil || rollback == nil {
		return errors.New("group move and rollback actions are required")
	}
	releaseOperation, err := lockmgr.Process.Acquire(
		lockmgr.GroupRequests(
			s.Root,
			[]string{sourceGroup, destinationGroup},
			lockmgr.Exclusive,
		)...,
	)
	if err != nil {
		return err
	}
	defer func() {
		releaseOperation()
	}()
	return s.MoveGroupLocked(sourceGroup, destinationGroup, move, rollback)
}

// MoveGroupLocked moves group and review state while its caller holds the
// repository operations lock.
func (s *Store) MoveGroupLocked(
	sourceGroup string,
	destinationGroup string,
	move func() error,
	rollback func() error,
) error {
	if move == nil || rollback == nil {
		return errors.New("group move and rollback actions are required")
	}
	unlock, err := s.lockGroups(sourceGroup, destinationGroup)
	if err != nil {
		return err
	}
	defer func() {
		_ = unlock()
	}()
	return s.moveGroup(sourceGroup, destinationGroup, move, rollback)
}

func (s *Store) moveGroup(
	sourceGroup string,
	destinationGroup string,
	move func() error,
	rollback func() error,
) error {
	if moveErr := move(); moveErr != nil {
		return moveErr
	}
	if rewriteErr := s.rewriteGroup(sourceGroup, destinationGroup); rewriteErr != nil {
		if rollbackErr := rollback(); rollbackErr != nil {
			return errors.Join(rewriteErr, fmt.Errorf("restore group directory: %w", rollbackErr))
		}
		return rewriteErr
	}
	return nil
}

func (s *Store) lockLifecycle() (func() error, error) {
	release, err := lockmgr.Process.Acquire(
		lockmgr.CatalogRequest(s.Root, lockmgr.Exclusive),
		lockmgr.ReviewCatalogRequest(s.Root, lockmgr.Exclusive),
	)
	if err != nil {
		return nil, err
	}
	return func() error {
		release()
		return nil
	}, nil
}
