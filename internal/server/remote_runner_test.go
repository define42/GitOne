package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/define42/GitOne/internal/repoconfig"
	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/runner"
	"github.com/define42/GitOne/internal/storage"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"gopkg.in/yaml.v3"
)

type remoteIntegrationExecutor struct{}

func (remoteIntegrationExecutor) Run(
	_ context.Context,
	request runner.ExecuteRequest,
	output io.Writer,
) error {
	if request.Job.Name != "test" {
		return errors.New("remote job name does not match")
	}
	contents, err := os.ReadFile(filepath.Join(request.Directory, "source.txt"))
	if err != nil {
		return err
	}
	if string(contents) != "remote source\n" {
		return errors.New("remote source contents do not match")
	}
	_, err = io.WriteString(output, "integration build passed\n")
	return err
}

func TestRemoteRunnerEndToEnd(t *testing.T) {
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
	gitPath, err := repositoryStore.GitPath(repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(t.TempDir(), "checkout")
	repository, err := git.PlainClone(checkout, false, &git.CloneOptions{URL: gitPath})
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := yaml.Marshal(repoconfig.Config{
		Jobs: map[string]repoconfig.JobConfig{"test": {
			Image: "alpine:3", Script: []string{"test -f source.txt"}, TimeoutSeconds: 30,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(
		filepath.Join(checkout, ".gitone.yaml"),
		configuration,
		0o640,
	); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(
		filepath.Join(checkout, "source.txt"),
		[]byte("remote source\n"),
		0o640,
	); err != nil {
		t.Fatal(err)
	}
	worktree, err := repository.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = worktree.Add(".gitone.yaml"); err != nil {
		t.Fatal(err)
	}
	if _, err = worktree.Add("source.txt"); err != nil {
		t.Fatal(err)
	}
	commit, err := worktree.Commit("Configure remote build", &git.CommitOptions{
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

	coordinator, err := runner.NewCoordinator(runner.CoordinatorConfig{
		Storage: repositoryStore,
		State:   runner.NewStore(root),
	})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := coordinator.Schedule(repositoryPath, "main", commit)
	if err != nil {
		t.Fatal(err)
	}
	job := jobs[0]
	handler := New(Config{
		Root:        root,
		Directory:   testLDAPDirectory(),
		Coordinator: coordinator,
		RunnerToken: "remote-integration-token",
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	unauthorized, err := http.Post(
		server.URL+"/api/runner/jobs/claim",
		"application/json",
		bytes.NewBufferString(`{"runnerId":"unauthorized"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	_ = unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized runner status = %d", unauthorized.StatusCode)
	}
	authorizedRequest, err := http.NewRequest(
		http.MethodPost,
		server.URL+"/api/runner/jobs/claim",
		bytes.NewBufferString(`{"runnerId":"api-probe"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	authorizedRequest.Header.Set("Authorization", "Bearer remote-integration-token")
	authorizedRequest.Header.Set("Content-Type", "application/json")
	authorized, err := server.Client().Do(authorizedRequest)
	if err != nil {
		t.Fatal(err)
	}
	var claimResponse struct {
		Lease *runner.Lease `json:"lease"`
	}
	if err = json.NewDecoder(authorized.Body).Decode(&claimResponse); err != nil {
		_ = authorized.Body.Close()
		t.Fatal(err)
	}
	_ = authorized.Body.Close()
	if authorized.StatusCode != http.StatusOK ||
		claimResponse.Lease == nil ||
		claimResponse.Lease.Job.ID != job.ID {
		t.Fatalf("authorized runner claim = %d, %#v", authorized.StatusCode, claimResponse)
	}
	if _, err = coordinator.Complete(repositoryPath, job.ID, "api-probe", "probe complete"); err != nil {
		t.Fatal(err)
	}
	jobs, err = coordinator.Schedule(repositoryPath, "main", commit)
	if err != nil {
		t.Fatal(err)
	}
	job = jobs[0]

	remote, err := runner.NewRemote(runner.RemoteConfig{
		URL: server.URL, Token: "remote-integration-token", ID: "remote-integration",
		WorkRoot: t.TempDir(), Workers: 1, PollInterval: 250 * time.Millisecond,
		HTTPClient: server.Client(), Executor: remoteIntegrationExecutor{},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- remote.Run(ctx)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case runErr := <-result:
			t.Fatalf("remote runner stopped early: %v", runErr)
		default:
		}
		current, getErr := coordinator.Store().Get(repositoryPath, job.ID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if current.Status == runner.StatusSucceeded {
			break
		}
		if current.Status == runner.StatusFailed {
			t.Fatalf("remote build failed: %s", current.Error)
		}
		if time.Now().After(deadline) {
			t.Fatalf("remote build did not finish: %#v", current)
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case <-result:
	case <-time.After(5 * time.Second):
		t.Fatal("remote runner did not stop")
	}
	logContents, err := coordinator.Store().Log(repositoryPath, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logContents, "GitOne remote build") ||
		!strings.Contains(logContents, "job: test") ||
		!strings.Contains(logContents, "needs: none") ||
		!strings.Contains(logContents, "integration build passed") {
		t.Fatalf("remote integration log = %q", logContents)
	}
}
