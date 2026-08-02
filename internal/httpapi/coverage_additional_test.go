package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/define42/GitOne/internal/control"
	"github.com/define42/GitOne/internal/repoconfig"
	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/runner"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"gopkg.in/yaml.v3"
)

func TestRunnerAPIValidationAndMissingJobs(t *testing.T) {
	service, _, _ := repositoryAPIFixture(t)
	coordinator, err := runner.NewCoordinator(runner.CoordinatorConfig{
		Storage:       service.Storage,
		State:         runner.NewStore(service.Storage.Root),
		LeaseDuration: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	service.Coordinator = coordinator
	service.RunnerToken = "runner-secret"
	const authorization = "Bearer runner-secret"

	for _, test := range []struct {
		name string
		call func() error
		want int
	}{
		{
			name: "heartbeat authentication",
			call: func() error {
				_, callErr := service.heartbeatRunnerJob(context.Background(), &runnerJobInput{})
				return callErr
			},
			want: http.StatusUnauthorized,
		},
		{
			name: "heartbeat repository",
			call: func() error {
				_, callErr := service.heartbeatRunnerJob(context.Background(), &runnerJobInput{
					Authorization: authorization,
					Body: runnerJobBody{
						RunnerID:   "runner-one",
						Repository: "invalid",
						ID:         "missing",
					},
				})
				return callErr
			},
			want: http.StatusBadRequest,
		},
		{
			name: "heartbeat missing job",
			call: func() error {
				_, callErr := service.heartbeatRunnerJob(context.Background(), &runnerJobInput{
					Authorization: authorization,
					Body: runnerJobBody{
						RunnerID:   "runner-one",
						Repository: "engineering/api",
						ID:         "missing",
					},
				})
				return callErr
			},
			want: http.StatusConflict,
		},
		{
			name: "append invalid repository",
			call: func() error {
				_, callErr := service.appendRunnerJobLog(context.Background(), &runnerLogInput{
					Authorization: authorization,
					Body:          runnerLogBody{Repository: "invalid"},
				})
				return callErr
			},
			want: http.StatusBadRequest,
		},
		{
			name: "append missing job",
			call: func() error {
				_, callErr := service.appendRunnerJobLog(context.Background(), &runnerLogInput{
					Authorization: authorization,
					Body: runnerLogBody{
						RunnerID:   "runner-one",
						Repository: "engineering/api",
						ID:         "missing",
					},
				})
				return callErr
			},
			want: http.StatusConflict,
		},
		{
			name: "complete invalid repository",
			call: func() error {
				_, callErr := service.completeRunnerJob(context.Background(), &runnerCompleteInput{
					Authorization: authorization,
					Body:          runnerCompleteBody{Repository: "invalid"},
				})
				return callErr
			},
			want: http.StatusBadRequest,
		},
		{
			name: "complete missing job",
			call: func() error {
				_, callErr := service.completeRunnerJob(context.Background(), &runnerCompleteInput{
					Authorization: authorization,
					Body: runnerCompleteBody{
						RunnerID:   "runner-one",
						Repository: "engineering/api",
						ID:         "missing",
					},
				})
				return callErr
			},
			want: http.StatusConflict,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			requireStatusError(t, test.call(), test.want)
		})
	}

	if _, err = service.claimRunnerJob(context.Background(), &runnerClaimInput{
		Authorization: authorization,
		Body:          runnerClaimBody{RunnerID: "invalid runner"},
	}); err == nil {
		t.Fatal("invalid runner ID claimed a build")
	}
	claim, err := service.claimRunnerJob(context.Background(), &runnerClaimInput{
		Authorization: authorization,
		Body:          runnerClaimBody{RunnerID: "runner-one"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if claim.Body.Lease != nil {
		t.Fatalf("empty queue returned lease %#v", claim.Body.Lease)
	}

	for _, test := range []struct {
		name          string
		api           API
		authorization string
		want          int
	}{
		{name: "disabled", api: API{}, want: http.StatusServiceUnavailable},
		{name: "missing bearer", api: service, want: http.StatusUnauthorized},
		{
			name: "wrong token", api: service,
			authorization: "Bearer wrong", want: http.StatusUnauthorized,
		},
	} {
		t.Run("authorize "+test.name, func(t *testing.T) {
			requireStatusError(
				t,
				test.api.authorizeRunner(test.authorization),
				test.want,
			)
		})
	}
	if err = service.authorizeRunner(authorization); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerSourceAndProblemResponses(t *testing.T) {
	service, _, _ := repositoryAPIFixture(t)
	coordinator, err := runner.NewCoordinator(runner.CoordinatorConfig{
		Storage: service.Storage,
		State:   runner.NewStore(service.Storage.Root),
	})
	if err != nil {
		t.Fatal(err)
	}
	service.Coordinator = coordinator
	service.RunnerToken = "runner-secret"

	for _, test := range []struct {
		name          string
		target        string
		authorization string
		want          int
	}{
		{
			name:   "unauthorized",
			target: "/api/runner/source",
			want:   http.StatusUnauthorized,
		},
		{
			name:          "invalid repository",
			target:        "/api/runner/source?repository=invalid",
			authorization: "Bearer runner-secret",
			want:          http.StatusBadRequest,
		},
		{
			name:          "missing job",
			target:        "/api/runner/source?repository=engineering%2Fapi&id=missing&runnerId=runner-one",
			authorization: "Bearer runner-secret",
			want:          http.StatusConflict,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.target, nil)
			request.Header.Set("Authorization", test.authorization)
			response := httptest.NewRecorder()
			service.runnerSource(response, request)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.want, response.Body.String())
			}
		})
	}

	response := httptest.NewRecorder()
	writeRunnerError(response, errors.New("internal"))
	if response.Code != http.StatusInternalServerError ||
		!strings.Contains(response.Body.String(), `"status":500`) {
		t.Fatalf("unexpected runner problem: %d %s", response.Code, response.Body.String())
	}
}

func TestRunnerAPIJobLifecycle(t *testing.T) {
	service, _, _ := repositoryAPIFixture(t)
	repository := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	commit := commitRunnerBuildConfig(t, service, repository)
	coordinator, err := runner.NewCoordinator(runner.CoordinatorConfig{
		Storage:       service.Storage,
		State:         runner.NewStore(service.Storage.Root),
		LeaseDuration: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	service.Coordinator = coordinator
	service.RunnerToken = "runner-secret"
	const authorization = "Bearer runner-secret"

	jobs, err := coordinator.Schedule(repository, "main", commit)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatal("build was not scheduled")
	}
	job := jobs[0]
	claim, err := service.claimRunnerJob(context.Background(), &runnerClaimInput{
		Authorization: authorization,
		Body:          runnerClaimBody{RunnerID: "runner-one"},
	})
	if err != nil || claim.Body.Lease == nil {
		t.Fatalf("claim = %#v, %v", claim, err)
	}

	heartbeat, err := service.heartbeatRunnerJob(context.Background(), &runnerJobInput{
		Authorization: authorization,
		Body: runnerJobBody{
			RunnerID: "runner-one", Repository: repository.Full(), ID: job.ID,
		},
	})
	if err != nil || heartbeat.Body.LeaseExpiresAt.IsZero() {
		t.Fatalf("heartbeat = %#v, %v", heartbeat, err)
	}
	logOutput, err := service.appendRunnerJobLog(context.Background(), &runnerLogInput{
		Authorization: authorization,
		Body: runnerLogBody{
			RunnerID: "runner-one", Repository: repository.Full(), ID: job.ID,
			Content: []byte("tests passed\n"),
		},
	})
	if err != nil || logOutput.Body.Offset != int64(len("tests passed\n")) {
		t.Fatalf("log output = %#v, %v", logOutput, err)
	}

	sourceRequest := httptest.NewRequest(
		http.MethodGet,
		"/api/runner/source?repository=engineering%2Fapi&id="+job.ID+"&runnerId=runner-one",
		nil,
	)
	sourceRequest.Header.Set("Authorization", authorization)
	sourceResponse := httptest.NewRecorder()
	service.runnerSource(sourceResponse, sourceRequest)
	if sourceResponse.Code != http.StatusOK ||
		sourceResponse.Header().Get("Content-Type") != "application/gzip" ||
		sourceResponse.Body.Len() == 0 {
		t.Fatalf("source response = %d, %d bytes", sourceResponse.Code, sourceResponse.Body.Len())
	}

	complete, err := service.completeRunnerJob(context.Background(), &runnerCompleteInput{
		Authorization: authorization,
		Body: runnerCompleteBody{
			RunnerID: "runner-one", Repository: repository.Full(), ID: job.ID,
		},
	})
	if err != nil || complete.Body.Build.Status != runner.StatusSucceeded {
		t.Fatalf("complete = %#v, %v", complete, err)
	}
}

func TestArchiveImportValidationAndProblemResponses(t *testing.T) {
	service, credentials, _ := repositoryAPIFixture(t)
	for _, test := range []struct {
		name        string
		path        string
		filename    string
		body        string
		credentials bool
		want        int
	}{
		{
			name: "invalid repository path",
			path: "invalid", filename: "repo.zip",
			want: http.StatusBadRequest,
		},
		{
			name: "unsupported filename",
			path: "engineering/new", filename: "repo.rar",
			want: http.StatusBadRequest,
		},
		{
			name: "unauthorized",
			path: "engineering/new", filename: "repo.zip",
			want: http.StatusUnauthorized,
		},
		{
			name: "empty archive",
			path: "engineering/new", filename: "repo.zip",
			credentials: true, want: http.StatusBadRequest,
		},
		{
			name: "invalid archive",
			path: "engineering/new", filename: "repo.zip", body: "not a zip",
			credentials: true, want: http.StatusBadRequest,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(
				http.MethodPost,
				"/api/repositories/"+test.path+"/import-archive?filename="+test.filename,
				strings.NewReader(test.body),
			)
			request.SetPathValue("path", test.path)
			if test.credentials {
				request.Header.Set("Authorization", credentials.Authorization)
			}
			response := httptest.NewRecorder()
			service.importRepositoryArchiveHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.want, response.Body.String())
			}
		})
	}

	response := httptest.NewRecorder()
	writeArchiveImportAPIError(response, errors.New("internal"))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("archive problem status = %d", response.Code)
	}
}

func TestArchiveImportReadAndTemporaryFileFailures(t *testing.T) {
	service, credentials, _ := repositoryAPIFixture(t)

	t.Run("temporary file", func(t *testing.T) {
		temporaryRoot := filepath.Join(t.TempDir(), "missing")
		t.Setenv("TMPDIR", temporaryRoot)
		request := httptest.NewRequest(
			http.MethodPost,
			"/api/repositories/engineering%2Fnew/import-archive?filename=repo.zip",
			strings.NewReader("archive"),
		)
		request.SetPathValue("path", "engineering/new")
		request.Header.Set("Authorization", credentials.Authorization)
		response := httptest.NewRecorder()
		service.importRepositoryArchiveHTTP(response, request)
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d: %s", response.Code, response.Body.String())
		}
	})

	t.Run("request body", func(t *testing.T) {
		request := httptest.NewRequest(
			http.MethodPost,
			"/api/repositories/engineering%2Fnew/import-archive?filename=repo.zip",
			errorReader{},
		)
		request.SetPathValue("path", "engineering/new")
		request.Header.Set("Authorization", credentials.Authorization)
		response := httptest.NewRecorder()
		service.importRepositoryArchiveHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status = %d: %s", response.Code, response.Body.String())
		}
	})
}

func TestRepositoryBuildEndpointErrors(t *testing.T) {
	service, credentials, commit := repositoryAPIFixture(t)
	ctx := context.Background()

	if _, err := service.listRepositoryBuilds(ctx, &repositoryBuildsInput{
		AuthInput: credentials, Repository: "invalid",
	}); err == nil {
		t.Fatal("invalid repository listed builds")
	}
	if _, err := service.listRepositoryBuilds(ctx, &repositoryBuildsInput{
		AuthInput: credentials, Repository: "engineering/api",
	}); err == nil {
		t.Fatal("disabled build store listed builds")
	}

	buildStore := runner.NewStore(service.Storage.Root)
	service.Builds = &buildStore
	if _, err := service.getRepositoryBuild(ctx, &repositoryBuildInput{
		AuthInput: credentials, Repository: "engineering/api", ID: "missing",
	}); err == nil {
		t.Fatal("missing build was returned")
	}

	directory := filepath.Join(buildStore.Root, "engineering", "api.build")
	if err := os.MkdirAll(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "invalid.json"), []byte("{"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := service.getRepositoryBuild(ctx, &repositoryBuildInput{
		AuthInput: credentials, Repository: "engineering/api", ID: "invalid",
	}); err == nil {
		t.Fatal("invalid build record was returned")
	}

	job := runner.Job{
		ID: "no-log", Repository: "engineering/api", Branch: "main", Commit: commit,
		Status: runner.StatusSucceeded, CreatedAt: time.Now().UTC(),
	}
	contents, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, job.ID+".json"), contents, 0o640); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(directory, job.ID+".log"), 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err = service.getRepositoryBuild(ctx, &repositoryBuildInput{
		AuthInput: credentials, Repository: "engineering/api", ID: job.ID,
	}); err == nil {
		t.Fatal("unreadable build log was returned")
	}
}

func TestTokenEquality(t *testing.T) {
	now := time.Now().UTC()
	later := now.Add(time.Hour)
	base := control.Token{
		Name: "deploy", Key: "ci", Hash: "hash", Role: control.RoleDeveloper,
	}
	if !tokensEqual(base, base) {
		t.Fatal("identical tokens differ")
	}
	changed := base
	changed.Name = "release"
	if tokensEqual(base, changed) {
		t.Fatal("different tokens compare equal")
	}
	left := base
	left.ExpiresAt = &now
	if tokensEqual(left, base) {
		t.Fatal("nil and non-nil expiry compare equal")
	}
	right := base
	right.ExpiresAt = &now
	if !tokensEqual(left, right) {
		t.Fatal("equal expiries differ")
	}
	right.ExpiresAt = &later
	if tokensEqual(left, right) {
		t.Fatal("different expiries compare equal")
	}
}

type recordingScheduler struct {
	calls int
	err   error
}

func (s *recordingScheduler) Schedule(
	repopath.Repository,
	string,
	plumbing.Hash,
) ([]runner.Job, error) {
	s.calls++
	return nil, s.err
}

type recordingLockedScheduler struct {
	recordingScheduler
	lockedCalls int
}

func (s *recordingLockedScheduler) ScheduleLocked(
	repopath.Repository,
	string,
	plumbing.Hash,
) ([]runner.Job, error) {
	s.lockedCalls++
	return nil, s.err
}

func TestScheduleBuildSelectsLockAwareScheduler(t *testing.T) {
	repository := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	commit := plumbing.NewHash(strings.Repeat("1", 40))

	(API{}).scheduleBuild(repository, "main", commit)

	regular := &recordingScheduler{}
	(API{Scheduler: regular}).scheduleBuild(repository, "main", commit)
	if regular.calls != 1 {
		t.Fatalf("regular scheduler calls = %d", regular.calls)
	}
	regular.err = errors.New("schedule failed")
	(API{Scheduler: regular}).scheduleBuild(repository, "main", commit)

	locked := &recordingLockedScheduler{}
	(API{Scheduler: locked}).scheduleBuild(repository, "main", commit)
	if locked.calls != 0 || locked.lockedCalls != 1 {
		t.Fatalf("locked scheduler calls = %d/%d", locked.calls, locked.lockedCalls)
	}
}

func requireStatusError(t *testing.T, err error, status int) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected HTTP %d error", status)
	}
	var statusError huma.StatusError
	if !errors.As(err, &statusError) || statusError.GetStatus() != status {
		t.Fatalf("error = %v, want HTTP %d", err, status)
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}

func commitRunnerBuildConfig(
	t *testing.T,
	service API,
	repositoryPath repopath.Repository,
	manual ...bool,
) plumbing.Hash {
	t.Helper()
	gitPath, err := service.Storage.GitPath(repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(t.TempDir(), "checkout")
	repository, err := git.PlainClone(checkout, false, &git.CloneOptions{URL: gitPath})
	if err != nil {
		t.Fatal(err)
	}
	isManual := len(manual) > 0 && manual[0]
	contents, err := yaml.Marshal(repoconfig.Config{
		Jobs: map[string]repoconfig.JobConfig{"test": {
			Image: "alpine:3", Script: []string{"true"}, Manual: isManual,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(
		filepath.Join(checkout, repoconfig.FileName),
		contents,
		0o640,
	); err != nil {
		t.Fatal(err)
	}
	worktree, err := repository.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = worktree.Add(repoconfig.FileName); err != nil {
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
	return commit
}
