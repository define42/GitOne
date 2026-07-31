package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/define42/GitOne/internal/repoconfig"
	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/storage"
)

type sourceCheckingExecutor struct{}

func (sourceCheckingExecutor) Run(
	_ context.Context,
	request ExecuteRequest,
	output io.Writer,
) error {
	contents, err := os.ReadFile(filepath.Join(request.Directory, "source.txt"))
	if err != nil {
		return err
	}
	_, err = output.Write(contents)
	return err
}

func TestRemoteRunnerClaimsDownloadsLogsAndCompletes(t *testing.T) {
	root := t.TempDir()
	repositoryPath := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	repositoryStore := storage.Store{Root: root}
	if err := repositoryStore.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	if err := repositoryStore.CreateRepository(repositoryPath, storage.CreateRepositoryOptions{
		InitializeReadme: true,
		Author:           "alice",
	}); err != nil {
		t.Fatal(err)
	}
	commit := commitBuildConfig(t, repositoryStore, repositoryPath, repoconfig.Config{
		Jobs: map[string]repoconfig.JobConfig{"test": {Image: "alpine:3", Script: []string{"true"}}},
	})
	var source bytes.Buffer
	if err := WriteSourceArchive(repositoryStore, repositoryPath, commit, &source); err != nil {
		t.Fatal(err)
	}

	job := Job{
		ID:         "remote-build",
		Repository: repositoryPath.Full(),
		Branch:     "main",
		Commit:     commit.String(),
		Image:      "alpine:3",
		Status:     StatusRunning,
		CreatedAt:  time.Now().UTC(),
		Attempt:    1,
		RunnerID:   "remote-one",
	}
	lease := Lease{
		Job: job,
		Config: repoconfig.JobConfig{
			Image: "alpine:3", Script: []string{"true"}, TimeoutSeconds: 30,
		},
		LeaseSeconds: 30,
	}
	var mu sync.Mutex
	claimed := false
	var logContents []byte
	completed := make(chan runnerCompleteRequest, 1)
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer test-runner-token" {
			http.Error(response, "unauthorized", http.StatusUnauthorized)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/runner/jobs/claim":
			mu.Lock()
			if !claimed {
				claimed = true
				_ = json.NewEncoder(response).Encode(map[string]any{"lease": lease})
			} else {
				_ = json.NewEncoder(response).Encode(map[string]any{"lease": nil})
			}
			mu.Unlock()
		case "/api/runner/source":
			response.Header().Set("Content-Type", "application/gzip")
			_, _ = response.Write(source.Bytes())
		case "/api/runner/jobs/log":
			var input runnerLogRequest
			if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
				http.Error(response, err.Error(), http.StatusBadRequest)
				return
			}
			mu.Lock()
			if input.Offset != int64(len(logContents)) {
				mu.Unlock()
				http.Error(response, "offset mismatch", http.StatusConflict)
				return
			}
			logContents = append(logContents, input.Content...)
			offset := len(logContents)
			mu.Unlock()
			_ = json.NewEncoder(response).Encode(map[string]any{"offset": offset})
		case "/api/runner/jobs/heartbeat":
			_ = json.NewEncoder(response).Encode(map[string]any{
				"leaseExpiresAt": time.Now().UTC().Add(30 * time.Second),
			})
		case "/api/runner/jobs/complete":
			var input runnerCompleteRequest
			if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
				http.Error(response, err.Error(), http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(response).Encode(map[string]any{"build": job})
			completed <- input
		default:
			http.NotFound(response, request)
		}
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	remote, err := NewRemote(RemoteConfig{
		URL: server.URL, Token: "test-runner-token", ID: "remote-one",
		WorkRoot: t.TempDir(), Workers: 1, PollInterval: 250 * time.Millisecond,
		HTTPClient: server.Client(), Executor: sourceCheckingExecutor{},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runResult := make(chan error, 1)
	go func() {
		runResult <- remote.Run(ctx)
	}()

	select {
	case completion := <-completed:
		if completion.ID != job.ID ||
			completion.Repository != repositoryPath.Full() ||
			completion.RunnerID != "remote-one" ||
			completion.Error != "" {
			t.Fatalf("completion = %#v", completion)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("remote build did not complete")
	}
	cancel()
	select {
	case <-runResult:
	case <-time.After(5 * time.Second):
		t.Fatal("remote runner did not stop")
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(string(logContents), "GitOne remote build") ||
		!strings.Contains(string(logContents), "source at scheduled commit") {
		t.Fatalf("remote log = %q", logContents)
	}
}
