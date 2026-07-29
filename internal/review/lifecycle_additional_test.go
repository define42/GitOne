package review

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/define42/GitOne/internal/repopath"
)

func TestLifecycleLockHelpers(t *testing.T) {
	store := NewStore(t.TempDir())
	actionErr := errors.New("action failed")
	repository := testRepository()

	for name, run := range map[string]func(func() error) error{
		"lifecycle":      store.WithLifecycleLock,
		"held lifecycle": store.WithLifecycleLockHeld,
		"repository": func(action func() error) error {
			return store.WithRepositoryLocks([]repopath.Repository{repository}, action)
		},
		"group": func(action func() error) error {
			return store.WithGroupLocks([]string{repository.Group()}, action)
		},
	} {
		t.Run(name+" rejects nil", func(t *testing.T) {
			if err := run(nil); err == nil {
				t.Fatal("nil lifecycle action was accepted")
			}
		})
		t.Run(name+" returns action error", func(t *testing.T) {
			if err := run(func() error { return actionErr }); !errors.Is(err, actionErr) {
				t.Fatalf("action error = %v", err)
			}
		})
	}

	unlock, err := store.lockLifecycle()
	if err != nil {
		t.Fatal(err)
	}
	if err = unlock(); err != nil {
		t.Fatal(err)
	}
}

func TestMoveRepositoryLifecycleSuccess(t *testing.T) {
	root := t.TempDir()
	source := testRepository()
	destination := repopath.Repository{
		Groups: []string{"engineering", "platform"},
		Name:   "service",
	}
	createTestGitStore(t, root, source)
	store := NewStore(root)
	request := testMergeRequest("Relocate")
	if err := store.Create(source, &request); err != nil {
		t.Fatal(err)
	}

	moved := false
	rolledBack := false
	if err := store.MoveRepository(
		source,
		destination,
		func() error {
			moved = true
			return nil
		},
		func() error {
			rolledBack = true
			return nil
		},
	); err != nil {
		t.Fatal(err)
	}
	if !moved || rolledBack {
		t.Fatalf("move=%t rollback=%t", moved, rolledBack)
	}
	persisted, err := store.Get(destination, request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Repository != destination.Full() {
		t.Fatalf("repository = %q, want %q", persisted.Repository, destination.Full())
	}

	if err = store.MoveRepositoryLocked(
		destination,
		destination,
		func() error { return nil },
		func() error { return nil },
	); err != nil {
		t.Fatalf("same-destination move: %v", err)
	}
}

func TestMoveRepositoryLifecycleFailures(t *testing.T) {
	store := NewStore(t.TempDir())
	source := testRepository()
	destination := repopath.Repository{
		Groups: []string{"engineering"},
		Name:   "service",
	}
	moveErr := errors.New("move failed")
	rollbackErr := errors.New("rollback failed")

	for _, test := range []struct {
		name         string
		move         func() error
		rollback     func() error
		wantRollback bool
	}{
		{
			name:         "move failure",
			move:         func() error { return moveErr },
			rollback:     func() error { return nil },
			wantRollback: false,
		},
		{
			name:         "move and rollback failure",
			move:         func() error { return moveErr },
			rollback:     func() error { return rollbackErr },
			wantRollback: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := store.MoveRepositoryLocked(
				source,
				destination,
				test.move,
				test.rollback,
			)
			if !errors.Is(err, moveErr) {
				t.Fatalf("move error = %v", err)
			}
			if errors.Is(err, rollbackErr) != test.wantRollback {
				t.Fatalf("rollback error presence = %t, want %t: %v",
					errors.Is(err, rollbackErr), test.wantRollback, err)
			}
		})
	}

	if err := store.MoveRepository(source, destination, nil, func() error { return nil }); err == nil {
		t.Fatal("public move accepted a nil move callback")
	}
	if err := store.MoveRepository(source, destination, func() error { return nil }, nil); err == nil {
		t.Fatal("public move accepted a nil rollback callback")
	}
	if err := store.MoveRepositoryLocked(source, destination, nil, func() error { return nil }); err == nil {
		t.Fatal("locked move accepted a nil move callback")
	}
	if err := store.MoveRepositoryLocked(source, destination, func() error { return nil }, nil); err == nil {
		t.Fatal("locked move accepted a nil rollback callback")
	}
}

func TestMoveRepositoryRelocationFailuresRollback(t *testing.T) {
	for _, rollbackFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "rollback succeeds", true: "rollback fails"}[rollbackFails],
			func(t *testing.T) {
				root := t.TempDir()
				store := NewStore(root)
				source := testRepository()
				destination := repopath.Repository{
					Groups: []string{"engineering"},
					Name:   "service",
				}
				directory, err := repositoryDirectory(root, source)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.MkdirAll(directory, 0o750); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(
					filepath.Join(directory, "1.json"),
					[]byte("{broken"),
					0o640,
				); err != nil {
					t.Fatal(err)
				}
				rollbackErr := errors.New("rollback failed")
				err = store.MoveRepositoryLocked(
					source,
					destination,
					func() error { return nil },
					func() error {
						if rollbackFails {
							return rollbackErr
						}
						return nil
					},
				)
				if err == nil || !strings.Contains(err.Error(), "read merge request") {
					t.Fatalf("relocation error = %v", err)
				}
				if errors.Is(err, rollbackErr) != rollbackFails {
					t.Fatalf("rollback error presence = %t, want %t: %v",
						errors.Is(err, rollbackErr), rollbackFails, err)
				}
			})
	}
}

func TestMoveGroupLifecycleSuccess(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	sourceGroup := "engineering"
	destinationGroup := "platform/backend"
	source := repopath.Repository{Groups: []string{sourceGroup}, Name: "api"}
	createTestGitStore(t, root, source)
	request := testMergeRequest("Move group")
	if err := store.Create(source, &request); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(root, sourceGroup)
	destinationPath := filepath.Join(root, "platform", "backend")

	if err := store.MoveGroup(
		sourceGroup,
		destinationGroup,
		func() error {
			if err := os.MkdirAll(filepath.Dir(destinationPath), 0o750); err != nil {
				return err
			}
			return os.Rename(sourcePath, destinationPath)
		},
		func() error { return os.Rename(destinationPath, sourcePath) },
	); err != nil {
		t.Fatal(err)
	}
	destination := repopath.Repository{
		Groups: []string{"platform", "backend"},
		Name:   "api",
	}
	persisted, err := store.Get(destination, request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Repository != destination.Full() {
		t.Fatalf("repository = %q, want %q", persisted.Repository, destination.Full())
	}
}

func TestMoveGroupLifecycleFailures(t *testing.T) {
	store := NewStore(t.TempDir())
	moveErr := errors.New("move failed")
	rollbackErr := errors.New("rollback failed")

	if err := store.MoveGroup(
		"engineering",
		"platform",
		nil,
		func() error { return nil },
	); err == nil {
		t.Fatal("public group move accepted a nil move callback")
	}
	if err := store.MoveGroup(
		"engineering",
		"platform",
		func() error { return nil },
		nil,
	); err == nil {
		t.Fatal("public group move accepted a nil rollback callback")
	}
	if err := store.MoveGroupLocked(
		"engineering",
		"platform",
		nil,
		func() error { return nil },
	); err == nil {
		t.Fatal("locked group move accepted a nil move callback")
	}
	if err := store.MoveGroupLocked(
		"engineering",
		"platform",
		func() error { return nil },
		nil,
	); err == nil {
		t.Fatal("locked group move accepted a nil rollback callback")
	}
	if err := store.MoveGroupLocked(
		"engineering",
		"platform",
		func() error { return moveErr },
		func() error { t.Fatal("rollback called after move failure"); return nil },
	); !errors.Is(err, moveErr) {
		t.Fatalf("move error = %v", err)
	}

	for _, rollbackFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "rollback succeeds", true: "rollback fails"}[rollbackFails],
			func(t *testing.T) {
				err := store.MoveGroupLocked(
					"engineering",
					"../invalid",
					func() error { return nil },
					func() error {
						if rollbackFails {
							return rollbackErr
						}
						return nil
					},
				)
				if err == nil || !strings.Contains(err.Error(), "invalid destination group") {
					t.Fatalf("rewrite error = %v", err)
				}
				if errors.Is(err, rollbackErr) != rollbackFails {
					t.Fatalf("rollback error presence = %t, want %t: %v",
						errors.Is(err, rollbackErr), rollbackFails, err)
				}
			})
	}
}
