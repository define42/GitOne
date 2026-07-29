package review

import (
	"errors"
	"fmt"

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
	unlock, err := s.lockStore()
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
	unlock, err := s.lockLifecycle()
	if err != nil {
		return err
	}
	defer func() {
		_ = unlock()
	}()
	return s.moveRepository(source, destination, move, rollback)
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
	unlock, err := s.lockStore()
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
	unlock, err := s.lockLifecycle()
	if err != nil {
		return err
	}
	defer func() {
		_ = unlock()
	}()
	return s.moveGroup(sourceGroup, destinationGroup, move, rollback)
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
	unlock, err := s.lockStore()
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
	releaseOperation, err := s.AcquireOperationLock()
	if err != nil {
		return nil, err
	}
	unlockStore, err := s.lockStore()
	if err != nil {
		_ = releaseOperation()
		return nil, err
	}
	return func() error {
		return errors.Join(unlockStore(), releaseOperation())
	}, nil
}
