package review

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/define42/GitOne/internal/repopath"
)

// AcquireMergeLock serializes merge transactions for a repository across
// GitOne processes that share the same storage root.
func (s *Store) AcquireMergeLock(repository repopath.Repository) (func() error, error) {
	if err := validateRepository(repository); err != nil {
		return nil, err
	}
	name := fmt.Sprintf("%x.merge.lock", sha256.Sum256([]byte(repository.Full())))
	return s.acquireFileLock(name)
}

// AcquireOperationLock prevents repository lifecycle changes while Git and
// review state are being inspected or updated as one operation.
func (s *Store) AcquireOperationLock() (func() error, error) {
	return s.acquireFileLock("operations.lock")
}

func (s *Store) acquireFileLock(name string) (func() error, error) {
	lockDirectory, err := repopath.SafeJoin(s.Root, ".review-locks")
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(lockDirectory, 0o750); err != nil {
		return nil, err
	}
	path := filepath.Join(lockDirectory, name)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o640)
	if err != nil {
		return nil, err
	}
	unlock, err := lockMergeFile(file)
	if err != nil {
		_ = file.Close()
		return nil, err
	}

	var once sync.Once
	var releaseErr error
	return func() error {
		once.Do(func() {
			releaseErr = errors.Join(unlock(), file.Close())
		})
		return releaseErr
	}, nil
}

func (s *Store) lockStore() (func() error, error) {
	s.mu.Lock()
	releaseFileLock, err := s.acquireFileLock("store.lock")
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	return func() error {
		err := releaseFileLock()
		s.mu.Unlock()
		return err
	}, nil
}
