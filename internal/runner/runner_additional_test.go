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
	"github.com/define42/GitOne/internal/review"
	"github.com/define42/GitOne/internal/storage"
	git "github.com/go-git/go-git/v5"
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

func TestGenerateJobIDUsesNanosecondsAndStrongRandomness(t *testing.T) {
	now := time.Date(2026, time.August, 1, 12, 34, 56, 123456789, time.UTC)
	random := bytes.Repeat([]byte{0xab}, jobIDRandomBytes)
	id, err := generateJobID(now, bytes.NewReader(random))
	if err != nil {
		t.Fatal(err)
	}
	want := "20260801T123456123456789-" + strings.Repeat("ab", jobIDRandomBytes)
	if id != want {
		t.Fatalf("build ID = %q, want %q", id, want)
	}
	if !validJobID(id) {
		t.Fatalf("generated invalid build ID %q", id)
	}
	if _, err = generateJobID(now, bytes.NewReader(random[:jobIDRandomBytes-1])); err == nil {
		t.Fatal("random-source failure produced a build ID")
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
		Jobs: map[string]repoconfig.JobConfig{"test": {Image: "alpine:3", Script: []string{"true"}}},
	})
	buildRunner := &Runner{
		storage: repositoryStore,
		state:   NewStore(root),
		jobs:    make(chan buildRequest),
	}
	jobs, err := buildRunner.Schedule(repositoryPath, "main", commit)
	if err == nil || !strings.Contains(err.Error(), "queue is full") ||
		len(jobs) != 1 || jobs[0].Status != StatusFailed || jobs[0].FinishedAt == nil {
		t.Fatalf("full queue builds = %#v, %v", jobs, err)
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
	if err != nil || len(failed) != 1 || failed[0].Status != StatusFailed {
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

func TestEmbeddedRunnerBuildWritesUseRepositoryOperationLock(t *testing.T) {
	root, repositoryPath, repositoryStore, _ := coordinatorRepository(t)
	commit := commitBuildConfig(t, repositoryStore, repositoryPath, repoconfig.Config{
		Jobs: map[string]repoconfig.JobConfig{"test": {Image: "alpine:3", Script: []string{"true"}}},
	})
	buildRunner := &Runner{
		storage: repositoryStore,
		state:   NewStore(root),
		jobs:    make(chan buildRequest, 4),
	}
	release, err := review.NewStore(root).AcquireOperationLock()
	if err != nil {
		t.Fatal(err)
	}
	lockedResult := make(chan error, 1)
	go func() {
		_, scheduleErr := buildRunner.ScheduleLocked(repositoryPath, "main", commit)
		lockedResult <- scheduleErr
	}()
	select {
	case err = <-lockedResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		_ = release()
		t.Fatal("embedded ScheduleLocked recursively acquired the operation lock")
	}
	if err = release(); err != nil {
		t.Fatal(err)
	}

	logFile, err := buildRunner.state.createLog(repositoryPath, "operation-log")
	if err != nil {
		t.Fatal(err)
	}
	if err = logFile.Close(); err != nil {
		t.Fatal(err)
	}
	writer := &operationBuildLogWriter{
		runner:     buildRunner,
		repository: repositoryPath,
		id:         "operation-log",
	}
	release, err = review.NewStore(root).AcquireOperationLock()
	if err != nil {
		t.Fatal(err)
	}
	writeResult := make(chan error, 1)
	go func() {
		_, writeErr := writer.Write([]byte("serialized log\n"))
		writeResult <- writeErr
	}()
	select {
	case err = <-writeResult:
		_ = release()
		t.Fatalf("embedded log write completed while operation lock was held: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err = release(); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-writeResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("embedded log write did not resume after operation lock release")
	}
	logContents, err := buildRunner.state.Log(repositoryPath, "operation-log")
	if err != nil {
		t.Fatal(err)
	}
	if logContents != "serialized log\n" {
		t.Fatalf("build log = %q", logContents)
	}
}

func TestEmbeddedRunnerRunReportsPreparationAndExecutorFailures(t *testing.T) {
	request := buildRequest{
		repository: repopath.Repository{Groups: []string{"engineering"}, Name: "api"},
		job: Job{
			ID: "build", Repository: "engineering/api", Branch: "main",
			Commit: strings.Repeat("1", 40), Status: StatusQueued, CreatedAt: time.Now().UTC(),
		},
		config: repoconfig.JobConfig{
			Image: "alpine:3", Script: []string{"true"}, TimeoutSeconds: 5,
		},
	}

	t.Run("initial state write", func(t *testing.T) {
		stateFile := filepath.Join(t.TempDir(), "state")
		if err := os.WriteFile(stateFile, []byte("not a directory"), 0o640); err != nil {
			t.Fatal(err)
		}
		buildRunner := &Runner{
			storage: initializeRunTestRepository(t, t.TempDir(), request.repository),
			state:   NewStore(stateFile),
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
			storage: initializeRunTestRepository(t, root, request.repository),
			state:   state,
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
			storage: initializeRunTestRepository(t, root, request.repository),
			state:   state,
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
			storage: initializeRunTestRepository(t, root, request.repository),
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

func initializeRunTestRepository(
	t *testing.T,
	root string,
	repository repopath.Repository,
) storage.Store {
	t.Helper()
	store := storage.Store{Root: root}
	path, err := store.GitPath(repository)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err = git.PlainInit(path, true); err != nil {
		t.Fatal(err)
	}
	return store
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
