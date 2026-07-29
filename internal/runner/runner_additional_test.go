package runner

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/define42/GitOne/internal/repoconfig"
	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/storage"
	"github.com/go-git/go-git/v5/plumbing"
)

func TestEmbeddedRunnerValidatesConfiguration(t *testing.T) {
	executor := executorFunc(func(context.Context, ExecuteRequest, io.Writer) error {
		return nil
	})
	tests := []struct {
		name   string
		config Config
	}{
		{name: "missing executor", config: Config{State: NewStore("state")}},
		{
			name:   "missing state",
			config: Config{Storage: storage.Store{Root: "data"}, Executor: executor},
		},
		{
			name:   "negative workers",
			config: Config{State: NewStore("state"), Executor: executor, Workers: -1},
		},
		{
			name:   "too many workers",
			config: Config{State: NewStore("state"), Executor: executor, Workers: 33},
		},
		{
			name: "small queue",
			config: Config{
				State: NewStore("state"), Executor: executor, Workers: 2, Queue: 1,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(test.config); err == nil {
				t.Fatal("invalid embedded runner configuration was accepted")
			}
		})
	}

	stateFile := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(stateFile, []byte("not a directory"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{
		Storage:  storage.Store{Root: t.TempDir()},
		State:    NewStore(stateFile),
		Executor: executor,
	}); err == nil {
		t.Fatal("state-directory creation failure was ignored")
	}
}

func TestEmbeddedRunnerScheduleErrorsAndFullQueue(t *testing.T) {
	root := t.TempDir()
	repositoryPath := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	repositoryStore := storage.Store{Root: root}
	if err := repositoryStore.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	if err := repositoryStore.CreateRepository(repositoryPath, storage.CreateRepositoryOptions{
		InitializeReadme: true, Author: "alice",
	}); err != nil {
		t.Fatal(err)
	}
	commit := commitBuildConfig(t, repositoryStore, repositoryPath, repoconfig.Config{
		Build: &repoconfig.BuildConfig{Image: "alpine:3", Script: []string{"true"}},
	})
	buildRunner := &Runner{
		storage: repositoryStore,
		state:   NewStore(root),
		jobs:    make(chan buildRequest),
	}
	job, err := buildRunner.Schedule(repositoryPath, "main", commit)
	if err == nil || !strings.Contains(err.Error(), "queue is full") ||
		job == nil || job.Status != StatusFailed || job.FinishedAt == nil {
		t.Fatalf("full queue build = %#v, %v", job, err)
	}

	if _, err = buildRunner.Schedule(
		repopath.Repository{Groups: []string{".."}, Name: "api"},
		"main",
		commit,
	); err == nil {
		t.Fatal("unsafe repository schedule was accepted")
	}
	if _, err = buildRunner.Schedule(
		repopath.Repository{Groups: []string{"engineering"}, Name: "missing"},
		"main",
		commit,
	); err == nil {
		t.Fatal("missing repository schedule was accepted")
	}
	failed, err := buildRunner.Schedule(repositoryPath, "main", plumbing.ZeroHash)
	if err != nil || failed == nil || failed.Status != StatusFailed {
		t.Fatalf("unknown commit schedule = %#v, %v", failed, err)
	}

	stateFile := filepath.Join(t.TempDir(), "state")
	if err = os.WriteFile(stateFile, []byte("not a directory"), 0o640); err != nil {
		t.Fatal(err)
	}
	brokenState := &Runner{
		storage: repositoryStore,
		state:   NewStore(stateFile),
		jobs:    make(chan buildRequest),
	}
	if _, err = brokenState.Schedule(repositoryPath, "main", commit); err == nil {
		t.Fatal("schedule state failure was ignored")
	}
}

func TestEmbeddedRunnerRunReportsPreparationAndExecutorFailures(t *testing.T) {
	request := buildRequest{
		repository: repopath.Repository{Groups: []string{"engineering"}, Name: "api"},
		job: Job{
			ID: "build", Repository: "engineering/api", Branch: "main",
			Commit: strings.Repeat("1", 40), Status: StatusQueued, CreatedAt: time.Now().UTC(),
		},
		config: repoconfig.BuildConfig{
			Image: "alpine:3", Script: []string{"true"}, TimeoutSeconds: 5,
		},
	}

	t.Run("initial state write", func(t *testing.T) {
		stateFile := filepath.Join(t.TempDir(), "state")
		if err := os.WriteFile(stateFile, []byte("not a directory"), 0o640); err != nil {
			t.Fatal(err)
		}
		buildRunner := &Runner{
			state: NewStore(stateFile),
			executor: executorFunc(func(context.Context, ExecuteRequest, io.Writer) error {
				return nil
			}),
		}
		buildRunner.run(context.Background(), request)
	})

	t.Run("log creation", func(t *testing.T) {
		root := t.TempDir()
		state := NewStore(root)
		directory, err := state.repositoryDirectory(request.repository)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.MkdirAll(
			filepath.Join(directory, request.job.ID+".log"),
			0o750,
		); err != nil {
			t.Fatal(err)
		}
		buildRunner := &Runner{
			state: state,
			executor: executorFunc(func(context.Context, ExecuteRequest, io.Writer) error {
				return nil
			}),
		}
		buildRunner.run(context.Background(), request)
		job, err := state.Get(request.repository, request.job.ID)
		if err != nil || job.Status != StatusFailed {
			t.Fatalf("log-creation build = %#v, %v", job, err)
		}
	})

	t.Run("workspace creation", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(
			filepath.Join(root, ".gitone"),
			[]byte("not a directory"),
			0o640,
		); err != nil {
			t.Fatal(err)
		}
		state := NewStore(root)
		buildRunner := &Runner{
			state: state,
			executor: executorFunc(func(context.Context, ExecuteRequest, io.Writer) error {
				return nil
			}),
		}
		buildRunner.run(context.Background(), request)
		job, err := state.Get(request.repository, request.job.ID)
		if err != nil || job.Status != StatusFailed {
			t.Fatalf("workspace build = %#v, %v", job, err)
		}
	})

	t.Run("checkout", func(t *testing.T) {
		root := t.TempDir()
		state := NewStore(root)
		buildRunner := &Runner{
			storage: storage.Store{Root: root},
			state:   state,
			executor: executorFunc(func(context.Context, ExecuteRequest, io.Writer) error {
				return nil
			}),
		}
		buildRunner.run(context.Background(), request)
		job, err := state.Get(request.repository, request.job.ID)
		if err != nil || job.Status != StatusFailed ||
			!strings.Contains(job.Error, "prepare build checkout") {
			t.Fatalf("checkout build = %#v, %v", job, err)
		}
	})
}

func TestEmbeddedRunnerCheckoutAndCappedWriterErrors(t *testing.T) {
	buildRunner := &Runner{storage: storage.Store{Root: t.TempDir()}}
	if err := buildRunner.checkout(
		repopath.Repository{Groups: []string{".."}, Name: "api"},
		plumbing.ZeroHash,
		t.TempDir(),
		io.Discard,
	); err == nil {
		t.Fatal("unsafe checkout repository was accepted")
	}
	if err := buildRunner.checkout(
		repopath.Repository{Groups: []string{"engineering"}, Name: "missing"},
		plumbing.ZeroHash,
		t.TempDir(),
		io.Discard,
	); err == nil || !strings.Contains(err.Error(), "prepare build checkout") {
		t.Fatalf("missing checkout error = %v", err)
	}

	writer := newCappedLogWriter(alwaysErrorWriter{}, 128)
	if written, err := writer.Write([]byte("small")); err == nil || written != 0 {
		t.Fatalf("direct writer error = %d, %v", written, err)
	}
	writer = newCappedLogWriter(alwaysErrorWriter{}, 1)
	if written, err := writer.Write(bytes.Repeat([]byte("x"), 32)); err == nil || written != 0 {
		t.Fatalf("marker writer error = %d, %v", written, err)
	}
}

func TestContainerExecutorRejectsInvalidBuildID(t *testing.T) {
	err := (ContainerExecutor{}).Run(
		context.Background(),
		ExecuteRequest{Job: Job{ID: "bad build"}},
		io.Discard,
	)
	if err == nil || !strings.Contains(err.Error(), "invalid build ID") {
		t.Fatalf("invalid build ID error = %v", err)
	}
}
