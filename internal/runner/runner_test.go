package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/define42/GitOne/internal/repoconfig"
	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/storage"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

type recordingExecutor struct {
	mu       sync.Mutex
	requests []ExecuteRequest
}

func (e *recordingExecutor) Run(
	_ context.Context,
	request ExecuteRequest,
	output io.Writer,
) error {
	e.mu.Lock()
	e.requests = append(e.requests, request)
	e.mu.Unlock()
	contents, err := os.ReadFile(filepath.Join(request.Directory, "source.txt"))
	if err != nil {
		return err
	}
	_, err = output.Write(contents)
	return err
}

func TestRunnerSchedulesExactCommitAndPersistsResult(t *testing.T) {
	root := t.TempDir()
	repositoryPath := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	store := storage.Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRepository(repositoryPath, storage.CreateRepositoryOptions{
		InitializeReadme: true,
		Author:           "alice",
	}); err != nil {
		t.Fatal(err)
	}
	commit := commitBuildConfig(t, store, repositoryPath, repoconfig.Config{
		Description: "API",
		Build: &repoconfig.BuildConfig{
			Image:          "golang:1.25",
			Script:         []string{"go test ./..."},
			Branches:       []string{"main"},
			TimeoutSeconds: 10,
		},
	})

	executor := &recordingExecutor{}
	buildRunner, err := New(Config{
		Storage:  store,
		State:    NewStore(root),
		Executor: executor,
		Workers:  1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer buildRunner.Close()
	job, err := buildRunner.Schedule(repositoryPath, "main", commit)
	if err != nil {
		t.Fatal(err)
	}
	if job == nil || job.Status != StatusQueued {
		t.Fatalf("unexpected scheduled job: %#v", job)
	}

	waitForJob(t, buildRunner.Store(), repositoryPath, job.ID, StatusSucceeded)
	log, err := buildRunner.Store().Log(repositoryPath, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log, "source at scheduled commit") {
		t.Fatalf("unexpected build log: %q", log)
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if len(executor.requests) != 1 ||
		executor.requests[0].Job.Commit != commit.String() ||
		executor.requests[0].Config.Image != "golang:1.25" {
		t.Fatalf("unexpected executor requests: %#v", executor.requests)
	}
}

func TestRunnerSkipsUnconfiguredAndFilteredBranches(t *testing.T) {
	root := t.TempDir()
	repositoryPath := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	store := storage.Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRepository(repositoryPath, storage.CreateRepositoryOptions{
		InitializeReadme: true,
		Author:           "alice",
	}); err != nil {
		t.Fatal(err)
	}
	gitPath, _ := store.GitPath(repositoryPath)
	repository, _ := git.PlainOpen(gitPath)
	head, _ := repository.Head()
	executor := &recordingExecutor{}
	buildRunner, err := New(Config{
		Storage: store, State: NewStore(root), Executor: executor,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer buildRunner.Close()
	if job, scheduleErr := buildRunner.Schedule(repositoryPath, "main", head.Hash()); scheduleErr != nil || job != nil {
		t.Fatalf("unconfigured job = %#v, %v", job, scheduleErr)
	}

	commit := commitBuildConfig(t, store, repositoryPath, repoconfig.Config{
		Build: &repoconfig.BuildConfig{
			Image: "alpine:3", Script: []string{"true"}, Branches: []string{"release/*"},
		},
	})
	if job, scheduleErr := buildRunner.Schedule(repositoryPath, "feature/docs", commit); scheduleErr != nil || job != nil {
		t.Fatalf("filtered job = %#v, %v", job, scheduleErr)
	}
}

func TestRunnerRecordsInvalidBuildConfiguration(t *testing.T) {
	root := t.TempDir()
	repositoryPath := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	store := storage.Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRepository(repositoryPath, storage.CreateRepositoryOptions{
		InitializeReadme: true,
		Author:           "alice",
	}); err != nil {
		t.Fatal(err)
	}
	commit := commitBuildConfig(t, store, repositoryPath, repoconfig.Config{
		Build: &repoconfig.BuildConfig{Image: "alpine:3"},
	})
	buildRunner, err := New(Config{
		Storage:  store,
		State:    NewStore(root),
		Executor: &recordingExecutor{},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer buildRunner.Close()
	job, err := buildRunner.Schedule(repositoryPath, "main", commit)
	if err != nil {
		t.Fatal(err)
	}
	if job == nil || job.Status != StatusFailed || !strings.Contains(job.Error, "script") {
		t.Fatalf("unexpected invalid-configuration job: %#v", job)
	}
}

func TestBuildStoreUsesRepositoryLocalDirectory(t *testing.T) {
	root := t.TempDir()
	repository := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	if err := os.MkdirAll(filepath.Join(root, "engineering", "api.git"), 0o750); err != nil {
		t.Fatal(err)
	}
	store := NewStore(root)
	job := Job{
		ID:         "build-local",
		Repository: repository.Full(),
		Branch:     "main",
		Commit:     strings.Repeat("1", 40),
		Status:     StatusSucceeded,
		CreatedAt:  time.Now().UTC(),
	}
	if err := store.save(repository, job); err != nil {
		t.Fatal(err)
	}
	logFile, err := store.createLog(repository, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = logFile.WriteString("tests passed\n"); err != nil {
		t.Fatal(err)
	}
	if err = logFile.Close(); err != nil {
		t.Fatal(err)
	}

	buildDirectory := filepath.Join(root, "engineering", "api.build")
	for _, name := range []string{job.ID + ".json", job.ID + ".log"} {
		if _, err = os.Stat(filepath.Join(buildDirectory, name)); err != nil {
			t.Fatalf("repository-local build artifact %s: %v", name, err)
		}
	}
	loaded, err := store.Get(repository, job.ID)
	if err != nil || loaded.ID != job.ID {
		t.Fatalf("repository-local build = %#v, %v", loaded, err)
	}
	log, err := store.Log(repository, job.ID)
	if err != nil || log != "tests passed\n" {
		t.Fatalf("repository-local log = %q, %v", log, err)
	}
}

func TestBuildLogIsCappedWithoutStoppingTheWriter(t *testing.T) {
	var output bytes.Buffer
	writer := newCappedLogWriter(&output, 64)
	first := strings.Repeat("a", 50)
	second := strings.Repeat("b", 50)
	if written, err := writer.Write([]byte(first)); err != nil || written != len(first) {
		t.Fatalf("first write = %d, %v", written, err)
	}
	if written, err := writer.Write([]byte(second)); err != nil || written != len(second) {
		t.Fatalf("second write = %d, %v", written, err)
	}
	if output.Len() > 64 || !strings.Contains(output.String(), "truncated by GitOne") {
		t.Fatalf("unexpected capped output (%d bytes): %q", output.Len(), output.String())
	}
	if written, err := writer.Write([]byte("discarded")); err != nil || written != len("discarded") {
		t.Fatalf("discarded write = %d, %v", written, err)
	}
}

func commitBuildConfig(
	t *testing.T,
	store storage.Store,
	repositoryPath repopath.Repository,
	config repoconfig.Config,
) plumbing.Hash {
	t.Helper()
	gitPath, err := store.GitPath(repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(t.TempDir(), "checkout")
	repository, err := git.PlainClone(checkout, false, &git.CloneOptions{URL: gitPath})
	if err != nil {
		t.Fatal(err)
	}
	contents, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(checkout, ".gitone.json"), append(contents, '\n'), 0o640); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(checkout, "source.txt"), []byte("source at scheduled commit\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	worktree, err := repository.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = worktree.Add(".gitone.json"); err != nil {
		t.Fatal(err)
	}
	if _, err = worktree.Add("source.txt"); err != nil {
		t.Fatal(err)
	}
	commit, err := worktree.Commit("Configure build", &git.CommitOptions{
		Author: &object.Signature{
			Name: "alice", Email: "alice@example.com", When: time.Now().UTC(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = repository.Push(&git.PushOptions{}); err != nil {
		t.Fatal(err)
	}
	return commit
}

func waitForJob(
	t *testing.T,
	store Store,
	repository repopath.Repository,
	id string,
	status Status,
) Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, err := store.Get(repository, id)
		if err == nil && job.Status == status {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	job, err := store.Get(repository, id)
	t.Fatalf("job did not reach %q: %#v, %v", status, job, err)
	return Job{}
}
