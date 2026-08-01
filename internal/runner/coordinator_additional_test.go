package runner

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/define42/GitOne/internal/lockmgr"
	"github.com/define42/GitOne/internal/repoconfig"
	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/review"
	"github.com/define42/GitOne/internal/storage"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

func TestNewCoordinatorValidatesConfigurationAndUsesDefaults(t *testing.T) {
	tests := []struct {
		name   string
		config CoordinatorConfig
	}{
		{name: "missing repository root", config: CoordinatorConfig{State: NewStore("state")}},
		{name: "missing state root", config: CoordinatorConfig{Storage: storage.Store{Root: "data"}}},
		{
			name: "short lease",
			config: CoordinatorConfig{
				Storage: storage.Store{Root: "data"},
				State:   NewStore("state"), LeaseDuration: 4 * time.Second,
			},
		},
		{
			name: "long lease",
			config: CoordinatorConfig{
				Storage: storage.Store{Root: "data"},
				State:   NewStore("state"), LeaseDuration: 11 * time.Minute,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewCoordinator(test.config); err == nil {
				t.Fatal("invalid coordinator configuration was accepted")
			}
		})
	}

	coordinator, err := NewCoordinator(CoordinatorConfig{
		Storage: storage.Store{Root: "data"},
		State:   NewStore("state"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if coordinator.leaseDuration != defaultLeaseDuration ||
		coordinator.Store().Root != "state" {
		t.Fatalf("coordinator defaults = %#v", coordinator)
	}
}

func TestCoordinatorScheduleOutcomes(t *testing.T) {
	root, repositoryPath, repositoryStore, coordinator := coordinatorRepository(t)
	gitPath, err := repositoryStore.GitPath(repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := git.PlainOpen(gitPath)
	if err != nil {
		t.Fatal(err)
	}
	head, err := repository.Head()
	if err != nil {
		t.Fatal(err)
	}
	if jobs, scheduleErr := coordinator.Schedule(
		repositoryPath,
		"main",
		head.Hash(),
	); scheduleErr != nil || len(jobs) != 0 {
		t.Fatalf("unconfigured schedule = %#v, %v", jobs, scheduleErr)
	}

	filteredCommit := commitBuildConfig(t, repositoryStore, repositoryPath, repoconfig.Config{
		Jobs: map[string]repoconfig.JobConfig{"test": {
			Image: "alpine:3", Script: []string{"true"}, Branches: []string{"release/*"},
		}},
	})
	if jobs, scheduleErr := coordinator.Schedule(
		repositoryPath,
		"feature/docs",
		filteredCommit,
	); scheduleErr != nil || len(jobs) != 0 {
		t.Fatalf("filtered schedule = %#v, %v", jobs, scheduleErr)
	}

	invalidCommit := commitBuildConfig(t, repositoryStore, repositoryPath, repoconfig.Config{
		Jobs: map[string]repoconfig.JobConfig{"test": {Image: "alpine:3"}},
	})
	failed, err := coordinator.Schedule(repositoryPath, "main", invalidCommit)
	if err != nil {
		t.Fatal(err)
	}
	if len(failed) != 1 || failed[0].Status != StatusFailed ||
		!strings.Contains(failed[0].Error, "script") {
		t.Fatalf("invalid configuration build = %#v", failed)
	}
	configurationLog, err := coordinator.Store().Log(repositoryPath, failed[0].ID)
	if err != nil || !strings.Contains(configurationLog, failed[0].Error) {
		t.Fatalf("configuration log = %q, %v", configurationLog, err)
	}

	badCommit, err := coordinator.Schedule(repositoryPath, "main", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	if len(badCommit) != 1 || badCommit[0].Status != StatusFailed {
		t.Fatalf("unknown commit build = %#v", badCommit)
	}

	if _, err = coordinator.Schedule(
		repopath.Repository{Groups: []string{".."}, Name: "api"},
		"main",
		head.Hash(),
	); err == nil {
		t.Fatal("unsafe repository schedule was accepted")
	}
	if _, err = coordinator.Schedule(
		repopath.Repository{Groups: []string{"engineering"}, Name: "missing"},
		"main",
		head.Hash(),
	); err == nil {
		t.Fatal("missing repository schedule was accepted")
	}

	stateRoot := filepath.Join(t.TempDir(), "state")
	if err = os.WriteFile(stateRoot, []byte("not a directory"), 0o640); err != nil {
		t.Fatal(err)
	}
	brokenState, err := NewCoordinator(CoordinatorConfig{
		Storage: repositoryStore,
		State:   NewStore(stateRoot),
	})
	if err != nil {
		t.Fatal(err)
	}
	validCommit := commitBuildConfig(t, repositoryStore, repositoryPath, repoconfig.Config{
		Jobs: map[string]repoconfig.JobConfig{"test": {Image: "alpine:3", Script: []string{"true"}}},
	})
	if _, err = brokenState.Schedule(repositoryPath, "main", validCommit); err == nil {
		t.Fatal("build state write failure was ignored")
	}

	if _, err = os.Stat(root); err != nil {
		t.Fatal(err)
	}
}

func TestCoordinatorSchedulesAndClaimsMultipleNamedJobs(t *testing.T) {
	_, repositoryPath, repositoryStore, coordinator := coordinatorRepository(t)
	commit := commitBuildConfig(t, repositoryStore, repositoryPath, repoconfig.Config{
		Jobs: map[string]repoconfig.JobConfig{
			"deploy": {
				Image: "alpine:3", Script: []string{"deploy"}, Manual: true,
			},
			"lint": {
				Image: "golang:1.26.5", Script: []string{"go vet ./..."},
			},
			"release": {
				Image: "alpine:3", Script: []string{"release"}, Branches: []string{"release/*"},
			},
		},
	})
	jobs, err := coordinator.Schedule(repositoryPath, "main", commit)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 || jobs[0].Name != "deploy" || jobs[0].Status != StatusManual ||
		jobs[1].Name != "lint" || jobs[1].Status != StatusQueued {
		t.Fatalf("scheduled jobs = %#v", jobs)
	}
	lease, err := coordinator.Claim("runner-one")
	if err != nil || lease == nil || lease.Job.Name != "lint" ||
		lease.Config.Script[0] != "go vet ./..." {
		t.Fatalf("automatic job lease = %#v, %v", lease, err)
	}
	if _, err = coordinator.Complete(repositoryPath, lease.Job.ID, "runner-one", ""); err != nil {
		t.Fatal(err)
	}
	if lease, err = coordinator.Claim("runner-one"); err != nil || lease != nil {
		t.Fatalf("manual job was claimed automatically: %#v, %v", lease, err)
	}
	if _, err = coordinator.Start(repositoryPath, jobs[0].ID); err != nil {
		t.Fatal(err)
	}
	lease, err = coordinator.Claim("runner-one")
	if err != nil || lease == nil || lease.Job.Name != "deploy" ||
		lease.Config.Script[0] != "deploy" {
		t.Fatalf("manual job lease = %#v, %v", lease, err)
	}
}

func TestCoordinatorRunsJobDependenciesInOrder(t *testing.T) {
	_, repositoryPath, repositoryStore, coordinator := coordinatorRepository(t)
	commit := commitBuildConfig(t, repositoryStore, repositoryPath, repoconfig.Config{
		Jobs: map[string]repoconfig.JobConfig{
			"test": {
				Image: "alpine:3", Script: []string{"test"},
			},
			"build": {
				Image: "alpine:3", Script: []string{"build"}, Needs: []string{"test"},
			},
			"deploy": {
				Image: "alpine:3", Script: []string{"deploy"}, Needs: []string{"build"}, Manual: true,
			},
		},
	})
	jobs, err := coordinator.Schedule(repositoryPath, "main", commit)
	if err != nil || len(jobs) != 3 {
		t.Fatalf("scheduled dependency jobs = %#v, %v", jobs, err)
	}
	byName := make(map[string]Job, len(jobs))
	for _, job := range jobs {
		byName[job.Name] = job
	}
	if byName["test"].Status != StatusQueued ||
		byName["build"].Status != StatusWaiting ||
		byName["deploy"].Status != StatusManual ||
		len(byName["build"].Needs) != 1 ||
		byName["build"].Needs[0].ID != byName["test"].ID ||
		len(byName["deploy"].Needs) != 1 ||
		byName["deploy"].Needs[0].ID != byName["build"].ID {
		t.Fatalf("dependency jobs = %#v", byName)
	}
	if _, err = coordinator.Start(repositoryPath, byName["deploy"].ID); !errors.Is(
		err,
		ErrBuildDependenciesUnmet,
	) {
		t.Fatalf("early manual start error = %v", err)
	}
	lease, err := coordinator.Claim("runner-one")
	if err != nil || lease == nil || lease.Job.Name != "test" {
		t.Fatalf("first dependency lease = %#v, %v", lease, err)
	}
	if _, err = coordinator.Complete(repositoryPath, lease.Job.ID, "runner-one", ""); err != nil {
		t.Fatal(err)
	}
	lease, err = coordinator.Claim("runner-one")
	if err != nil || lease == nil || lease.Job.Name != "build" {
		t.Fatalf("second dependency lease = %#v, %v", lease, err)
	}
	if _, err = coordinator.Complete(repositoryPath, lease.Job.ID, "runner-one", ""); err != nil {
		t.Fatal(err)
	}
	started, err := coordinator.Start(repositoryPath, byName["deploy"].ID)
	if err != nil || started.Status != StatusQueued {
		t.Fatalf("manual dependent start = %#v, %v", started, err)
	}
	lease, err = coordinator.Claim("runner-one")
	if err != nil || lease == nil || lease.Job.Name != "deploy" {
		t.Fatalf("manual dependency lease = %#v, %v", lease, err)
	}
}

func TestCoordinatorReconciliationDoesNotResurrectCanceledJob(t *testing.T) {
	_, repositoryPath, _, coordinator := coordinatorRepository(t)
	created := time.Now().UTC()
	dependency := Job{
		ID:         "dependency",
		Name:       "test",
		Repository: repositoryPath.Full(),
		Status:     StatusRunning,
		CreatedAt:  created,
		RunnerID:   "runner-one",
	}
	dependent := Job{
		ID:         "dependent",
		Name:       "build",
		Repository: repositoryPath.Full(),
		Needs:      []JobDependency{{Name: dependency.Name, ID: dependency.ID}},
		Status:     StatusWaiting,
		CreatedAt:  created,
	}
	for _, job := range []Job{dependency, dependent} {
		if err := coordinator.state.save(repositoryPath, job); err != nil {
			t.Fatal(err)
		}
	}

	releaseDependent, err := lockmgr.Process.Acquire(
		lockmgr.JobRequest(coordinator.storage.Root, repositoryPath, dependent.ID),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseDependent()

	completeResult := make(chan error, 1)
	go func() {
		_, completeErr := coordinator.Complete(
			repositoryPath,
			dependency.ID,
			"runner-one",
			"",
		)
		completeResult <- completeErr
	}()

	deadline := time.After(2 * time.Second)
	for {
		stored, getErr := coordinator.state.Get(repositoryPath, dependency.ID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if stored.Status == StatusSucceeded {
			break
		}
		select {
		case completeErr := <-completeResult:
			t.Fatalf("completion did not lock dependent job: %v", completeErr)
		case <-deadline:
			t.Fatal("dependency completion was not persisted")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	select {
	case completeErr := <-completeResult:
		t.Fatalf("completion did not wait for dependent job lock: %v", completeErr)
	case <-time.After(50 * time.Millisecond):
	}

	// This is the state mutation Cancel performs while it owns the dependent
	// job lock. Keeping the test's lock makes the stale-write interleaving
	// deterministic.
	finished := time.Now().UTC()
	dependent.Status = StatusCanceled
	dependent.FinishedAt = &finished
	if err = coordinator.state.save(repositoryPath, dependent); err != nil {
		t.Fatal(err)
	}
	releaseDependent()
	select {
	case err = <-completeResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("completion did not resume after dependent job lock was released")
	}

	stored, err := coordinator.state.Get(repositoryPath, dependent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusCanceled || stored.FinishedAt == nil {
		t.Fatalf("canceled dependent job was resurrected: %#v", stored)
	}
}

func TestCoordinatorFailsJobsWhoseDependenciesDoNotSucceed(t *testing.T) {
	_, repositoryPath, repositoryStore, coordinator := coordinatorRepository(t)
	commit := commitBuildConfig(t, repositoryStore, repositoryPath, repoconfig.Config{
		Jobs: map[string]repoconfig.JobConfig{
			"test":  {Image: "alpine:3", Script: []string{"test"}},
			"build": {Image: "alpine:3", Script: []string{"build"}, Needs: []string{"test"}},
			"deploy": {
				Image: "alpine:3", Script: []string{"deploy"}, Needs: []string{"build"},
			},
		},
	})
	jobs, err := coordinator.Schedule(repositoryPath, "main", commit)
	if err != nil || len(jobs) != 3 {
		t.Fatalf("scheduled dependency jobs = %#v, %v", jobs, err)
	}
	byName := make(map[string]Job, len(jobs))
	for _, job := range jobs {
		byName[job.Name] = job
	}
	if _, err = coordinator.Cancel(repositoryPath, byName["test"].ID); err != nil {
		t.Fatal(err)
	}
	if lease, claimErr := coordinator.Claim("runner-one"); claimErr != nil || lease != nil {
		t.Fatalf("failed dependencies became claimable: %#v, %v", lease, claimErr)
	}
	for _, name := range []string{"build", "deploy"} {
		stored, getErr := coordinator.Store().Get(repositoryPath, byName[name].ID)
		if getErr != nil || stored.Status != StatusFailed || stored.FinishedAt == nil ||
			!strings.Contains(stored.Error, "dependency") {
			t.Fatalf("failed dependent %s = %#v, %v", name, stored, getErr)
		}
	}
	rerunTest, err := coordinator.Rerun(repositoryPath, byName["test"].ID)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := coordinator.Claim("runner-one")
	if err != nil || lease == nil || lease.Job.ID != rerunTest.ID {
		t.Fatalf("rerun dependency lease = %#v, %v", lease, err)
	}
	if _, err = coordinator.Complete(repositoryPath, lease.Job.ID, "runner-one", ""); err != nil {
		t.Fatal(err)
	}
	rerunBuild, err := coordinator.Rerun(repositoryPath, byName["build"].ID)
	if err != nil || len(rerunBuild.Needs) != 1 || rerunBuild.Needs[0].ID != rerunTest.ID {
		t.Fatalf("rerun dependent = %#v, %v", rerunBuild, err)
	}
	lease, err = coordinator.Claim("runner-one")
	if err != nil || lease == nil || lease.Job.ID != rerunBuild.ID {
		t.Fatalf("rerun dependent lease = %#v, %v", lease, err)
	}
}

func TestCoordinatorFailsDependencyExcludedByBranchFilter(t *testing.T) {
	_, repositoryPath, repositoryStore, coordinator := coordinatorRepository(t)
	commit := commitBuildConfig(t, repositoryStore, repositoryPath, repoconfig.Config{
		Jobs: map[string]repoconfig.JobConfig{
			"test": {
				Image: "alpine:3", Script: []string{"test"}, Branches: []string{"main"},
			},
			"release": {
				Image: "alpine:3", Script: []string{"release"},
				Branches: []string{"release/*"}, Needs: []string{"test"},
			},
		},
	})
	jobs, err := coordinator.Schedule(repositoryPath, "release/1.0", commit)
	if err != nil || len(jobs) != 1 || jobs[0].Name != "release" ||
		jobs[0].Status != StatusFailed ||
		!strings.Contains(jobs[0].Error, `dependency "test" does not run`) {
		t.Fatalf("branch-filtered dependency jobs = %#v, %v", jobs, err)
	}
}

func TestCoordinatorScheduleOperationLockVariants(t *testing.T) {
	root, repositoryPath, repositoryStore, coordinator := coordinatorRepository(t)
	commit := commitBuildConfig(t, repositoryStore, repositoryPath, repoconfig.Config{
		Jobs: map[string]repoconfig.JobConfig{"test": {Image: "alpine:3", Script: []string{"true"}}},
	})

	release, err := review.NewStore(root).AcquireOperationLock()
	if err != nil {
		t.Fatal(err)
	}
	lockedResult := make(chan error, 1)
	go func() {
		_, scheduleErr := coordinator.ScheduleLocked(repositoryPath, "main", commit)
		lockedResult <- scheduleErr
	}()
	select {
	case err = <-lockedResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		_ = release()
		t.Fatal("ScheduleLocked recursively acquired the operation lock")
	}
	if err = release(); err != nil {
		t.Fatal(err)
	}

	release, err = review.NewStore(root).AcquireOperationLock()
	if err != nil {
		t.Fatal(err)
	}
	scheduleResult := make(chan error, 1)
	go func() {
		_, scheduleErr := coordinator.Schedule(repositoryPath, "main", commit)
		scheduleResult <- scheduleErr
	}()
	select {
	case err = <-scheduleResult:
		_ = release()
		t.Fatalf("standalone schedule completed while operation lock was held: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err = release(); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-scheduleResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("standalone schedule did not resume after operation lock release")
	}
}

func TestCoordinatorBuildMutationWaitsAndRevalidatesRepository(t *testing.T) {
	root, repositoryPath, repositoryStore, coordinator := coordinatorRepository(t)
	commit := commitBuildConfig(t, repositoryStore, repositoryPath, repoconfig.Config{
		Jobs: map[string]repoconfig.JobConfig{"test": {Image: "alpine:3", Script: []string{"true"}}},
	})
	jobs, err := coordinator.Schedule(repositoryPath, "main", commit)
	if err != nil {
		t.Fatal(err)
	}
	job := jobs[0]
	lease, err := coordinator.Claim("runner-one")
	if err != nil {
		t.Fatal(err)
	}
	if lease == nil || lease.Job.ID != job.ID {
		t.Fatalf("lease = %#v, want job %s", lease, job.ID)
	}

	release, err := review.NewStore(root).AcquireOperationLock()
	if err != nil {
		t.Fatal(err)
	}
	appendResult := make(chan error, 1)
	go func() {
		_, appendErr := coordinator.AppendLog(
			repositoryPath,
			job.ID,
			"runner-one",
			0,
			[]byte("build output\n"),
		)
		appendResult <- appendErr
	}()
	select {
	case err = <-appendResult:
		_ = release()
		t.Fatalf("log append completed while operation lock was held: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err = release(); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-appendResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("log append did not resume after operation lock release")
	}

	release, err = review.NewStore(root).AcquireOperationLock()
	if err != nil {
		t.Fatal(err)
	}
	heartbeatResult := make(chan error, 1)
	go func() {
		_, heartbeatErr := coordinator.Heartbeat(repositoryPath, job.ID, "runner-one")
		heartbeatResult <- heartbeatErr
	}()
	gitPath, err := repositoryStore.GitPath(repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	buildPath, err := repositoryStore.BuildPath(repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(gitPath, gitPath+".moved"); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(buildPath, buildPath+".moved"); err != nil {
		t.Fatal(err)
	}
	if err = release(); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-heartbeatResult:
		if err == nil {
			t.Fatal("heartbeat accepted a repository that moved while waiting")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("heartbeat did not finish after operation lock release")
	}
	if _, err = os.Stat(buildPath); !os.IsNotExist(err) {
		t.Fatalf("heartbeat recreated the moved build directory: %v", err)
	}
}

func TestCoordinatorOwnershipHeartbeatSourceAndFailureCompletion(t *testing.T) {
	_, repositoryPath, repositoryStore, coordinator := coordinatorRepository(t)
	commit := commitBuildConfig(t, repositoryStore, repositoryPath, repoconfig.Config{
		Jobs: map[string]repoconfig.JobConfig{"test": {Image: "alpine:3", Script: []string{"false"}}},
	})
	jobs, err := coordinator.Schedule(repositoryPath, "main", commit)
	if err != nil {
		t.Fatal(err)
	}
	job := jobs[0]
	if _, err = coordinator.Claim("invalid runner"); err == nil {
		t.Fatal("invalid runner claimed a job")
	}
	lease, err := coordinator.Claim("runner-one")
	if err != nil {
		t.Fatal(err)
	}
	if lease == nil {
		t.Fatal("job was not leased")
	}
	source, err := coordinator.SourceJob(repositoryPath, job.ID, "runner-one")
	if err != nil || source.ID != job.ID {
		t.Fatalf("source job = %#v, %v", source, err)
	}
	previousExpiry := *lease.Job.LeaseExpiresAt
	expiry, err := coordinator.Heartbeat(repositoryPath, job.ID, "runner-one")
	if err != nil || !expiry.After(previousExpiry) {
		t.Fatalf("heartbeat expiry = %s, %v", expiry, err)
	}
	failed, err := coordinator.Complete(
		repositoryPath,
		job.ID,
		"runner-one",
		"tests failed",
	)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != StatusFailed || failed.Error != "tests failed" ||
		failed.FinishedAt == nil || failed.LeaseExpiresAt != nil {
		t.Fatalf("failed completion = %#v", failed)
	}

	errorTests := []struct {
		name     string
		id       string
		runnerID string
		message  string
	}{
		{name: "invalid runner", id: job.ID, runnerID: "bad runner", message: "invalid runner ID"},
		{name: "missing build", id: "missing", runnerID: "runner-one", message: "build not found"},
		{name: "finished build", id: job.ID, runnerID: "runner-one", message: "not running"},
	}
	for _, test := range errorTests {
		t.Run(test.name, func(t *testing.T) {
			if _, sourceErr := coordinator.SourceJob(
				repositoryPath,
				test.id,
				test.runnerID,
			); sourceErr == nil || !strings.Contains(sourceErr.Error(), test.message) {
				t.Fatalf("source error = %v", sourceErr)
			}
		})
	}

	secondJobs, err := coordinator.Schedule(repositoryPath, "main", commit)
	if err != nil {
		t.Fatal(err)
	}
	secondLease, err := coordinator.Claim("runner-two")
	if err != nil {
		t.Fatal(err)
	}
	second := secondJobs[0]
	if secondLease == nil || secondLease.Job.ID != second.ID {
		t.Fatalf("second lease = %#v", secondLease)
	}
	if _, err = coordinator.SourceJob(
		repositoryPath,
		second.ID,
		"runner-one",
	); err == nil || !strings.Contains(err.Error(), "another runner") {
		t.Fatalf("wrong owner error = %v", err)
	}
}

func TestCoordinatorRerunAndCancellationLifecycle(t *testing.T) {
	_, repositoryPath, repositoryStore, coordinator := coordinatorRepository(t)
	commit := commitBuildConfig(t, repositoryStore, repositoryPath, repoconfig.Config{
		Jobs: map[string]repoconfig.JobConfig{"test": {
			Image: "alpine:3", Script: []string{"true"}, Branches: []string{"main"},
		}},
	})

	queuedJobs, err := coordinator.Schedule(repositoryPath, "main", commit)
	if err != nil || len(queuedJobs) != 1 {
		t.Fatalf("scheduled builds = %#v, %v", queuedJobs, err)
	}
	queued := queuedJobs[0]
	canceled, err := coordinator.Cancel(repositoryPath, queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if canceled.Status != StatusCanceled || canceled.FinishedAt == nil ||
		canceled.LeaseExpiresAt != nil {
		t.Fatalf("canceled queued build = %#v", canceled)
	}
	if repeated, cancelErr := coordinator.Cancel(repositoryPath, queued.ID); cancelErr != nil ||
		repeated.Status != StatusCanceled || !repeated.FinishedAt.Equal(*canceled.FinishedAt) {
		t.Fatalf("repeated cancellation = %#v, %v", repeated, cancelErr)
	}
	if lease, claimErr := coordinator.Claim("runner-one"); claimErr != nil || lease != nil {
		t.Fatalf("canceled build was claimable: %#v, %v", lease, claimErr)
	}

	rerun, err := coordinator.Rerun(repositoryPath, queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rerun.ID == queued.ID || rerun.RerunOf != queued.ID ||
		rerun.Status != StatusQueued || rerun.Repository != queued.Repository ||
		rerun.Name != queued.Name ||
		rerun.Branch != queued.Branch || rerun.Commit != queued.Commit ||
		rerun.Image != "alpine:3" {
		t.Fatalf("rerun build = %#v", rerun)
	}
	storedOriginal, err := coordinator.Store().Get(repositoryPath, queued.ID)
	if err != nil || storedOriginal.Status != StatusCanceled {
		t.Fatalf("original build changed = %#v, %v", storedOriginal, err)
	}
	lease, err := coordinator.Claim("runner-one")
	if err != nil || lease == nil || lease.Job.ID != rerun.ID {
		t.Fatalf("rerun claim = %#v, %v", lease, err)
	}
	if _, err = coordinator.Rerun(repositoryPath, rerun.ID); !errors.Is(
		err,
		ErrBuildNotRerunnable,
	) {
		t.Fatalf("running rerun error = %v", err)
	}

	canceled, err = coordinator.Cancel(repositoryPath, rerun.ID)
	if err != nil || canceled.Status != StatusCanceled || canceled.RunnerID != "runner-one" {
		t.Fatalf("canceled running build = %#v, %v", canceled, err)
	}
	heartbeat, err := coordinator.HeartbeatState(repositoryPath, rerun.ID, "runner-one")
	if err != nil || !heartbeat.Canceled ||
		!heartbeat.LeaseExpiresAt.Equal(*canceled.FinishedAt) {
		t.Fatalf("canceled heartbeat = %#v, %v", heartbeat, err)
	}
	completed, err := coordinator.Complete(repositoryPath, rerun.ID, "runner-one", "context canceled")
	if err != nil || completed.Status != StatusCanceled || completed.Error != "" {
		t.Fatalf("completion overwrote cancellation = %#v, %v", completed, err)
	}
	if _, err = coordinator.AppendLog(
		repositoryPath,
		rerun.ID,
		"runner-one",
		0,
		[]byte("late output"),
	); err == nil {
		t.Fatal("canceled build accepted log output")
	}
	if _, err = coordinator.Rerun(repositoryPath, "missing"); !errors.Is(err, ErrBuildNotFound) {
		t.Fatalf("missing rerun error = %v", err)
	}
}

func TestCoordinatorManualBuildRequiresExplicitStart(t *testing.T) {
	_, repositoryPath, repositoryStore, coordinator := coordinatorRepository(t)
	commit := commitBuildConfig(t, repositoryStore, repositoryPath, repoconfig.Config{
		Jobs: map[string]repoconfig.JobConfig{"deploy": {
			Image: "alpine:3", Script: []string{"true"}, Manual: true,
		}},
	})
	jobs, err := coordinator.Schedule(repositoryPath, "main", commit)
	if err != nil || len(jobs) != 1 || jobs[0].Status != StatusManual {
		t.Fatalf("manual builds = %#v, %v", jobs, err)
	}
	job := jobs[0]
	if lease, claimErr := coordinator.Claim("runner-one"); claimErr != nil || lease != nil {
		t.Fatalf("manual build was claimed before start: %#v, %v", lease, claimErr)
	}
	started, err := coordinator.Start(repositoryPath, job.ID)
	if err != nil || started.Status != StatusQueued || started.StartedAt != nil {
		t.Fatalf("started manual build = %#v, %v", started, err)
	}
	if _, err = coordinator.Start(repositoryPath, job.ID); !errors.Is(err, ErrBuildNotStartable) {
		t.Fatalf("repeated start error = %v", err)
	}
	lease, err := coordinator.Claim("runner-one")
	if err != nil || lease == nil || lease.Job.ID != job.ID ||
		lease.Job.Status != StatusRunning {
		t.Fatalf("started manual lease = %#v, %v", lease, err)
	}
	if _, err = coordinator.Start(repositoryPath, "missing"); !errors.Is(err, ErrBuildNotFound) {
		t.Fatalf("missing manual build error = %v", err)
	}
}

func TestCoordinatorRejectsUnbuildableCandidateAndStorageFailures(t *testing.T) {
	root, repositoryPath, _, coordinator := coordinatorRepository(t)
	unbuildable := Job{
		ID:         "unbuildable",
		Repository: repositoryPath.Full(),
		Branch:     "main",
		Commit:     strings.Repeat("f", 40),
		Status:     StatusQueued,
		CreatedAt:  time.Now().UTC(),
	}
	if err := coordinator.state.save(repositoryPath, unbuildable); err != nil {
		t.Fatal(err)
	}
	lease, err := coordinator.Claim("runner-one")
	if err != nil || lease != nil {
		t.Fatalf("unbuildable claim = %#v, %v", lease, err)
	}
	stored, err := coordinator.state.Get(repositoryPath, unbuildable.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusFailed || stored.FinishedAt == nil ||
		!strings.Contains(stored.Error, "prepare remote build") {
		t.Fatalf("unbuildable result = %#v", stored)
	}

	storageRoot := filepath.Join(t.TempDir(), "storage")
	if err = os.WriteFile(storageRoot, []byte("not a directory"), 0o640); err != nil {
		t.Fatal(err)
	}
	brokenStorage, err := NewCoordinator(CoordinatorConfig{
		Storage: storage.Store{Root: storageRoot},
		State:   NewStore(t.TempDir()),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = brokenStorage.Claim("runner-one"); err == nil {
		t.Fatal("storage listing failure was ignored")
	}

	buildDirectory := filepath.Join(root, "engineering", "api.build")
	if err = os.WriteFile(
		filepath.Join(buildDirectory, "broken.json"),
		[]byte("{"),
		0o640,
	); err != nil {
		t.Fatal(err)
	}
	if _, err = coordinator.Claim("runner-one"); err == nil {
		t.Fatal("build state listing failure was ignored")
	}
}

func TestCoordinatorJobConfigurationAndValidationHelpers(t *testing.T) {
	root, repositoryPath, repositoryStore, coordinator := coordinatorRepository(t)
	gitPath, err := repositoryStore.GitPath(repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := git.PlainOpen(gitPath)
	if err != nil {
		t.Fatal(err)
	}
	head, err := repository.Head()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = coordinator.jobConfig(repositoryPath, Job{
		ID: "missing-config", Commit: head.Hash().String(),
	}); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing build config error = %v", err)
	}
	if _, err = coordinator.jobConfig(
		repopath.Repository{Groups: []string{"engineering"}, Name: "missing"},
		Job{ID: "missing-repository", Commit: head.Hash().String()},
	); err == nil {
		t.Fatal("missing repository build configuration was accepted")
	}
	if _, err = coordinator.jobConfig(
		repopath.Repository{Groups: []string{".."}, Name: "api"},
		Job{ID: "unsafe-repository", Commit: head.Hash().String()},
	); err == nil {
		t.Fatal("unsafe repository build configuration was accepted")
	}
	if _, err = coordinator.jobConfig(repositoryPath, Job{
		ID: "missing-commit", Commit: strings.Repeat("f", 40),
	}); err == nil {
		t.Fatal("missing commit build configuration was accepted")
	}

	invalidCommit := commitBuildConfig(t, repositoryStore, repositoryPath, repoconfig.Config{
		Jobs: map[string]repoconfig.JobConfig{"test": {Image: "alpine:3"}},
	})
	if _, err = coordinator.jobConfig(repositoryPath, Job{
		ID: "invalid-config", Name: "test", Commit: invalidCommit.String(),
	}); err == nil || !strings.Contains(err.Error(), "script") {
		t.Fatalf("invalid build config error = %v", err)
	}

	for _, id := range []string{"", "bad id", strings.Repeat("x", 101)} {
		if validRunnerID(id) {
			t.Fatalf("validRunnerID(%q) = true", id)
		}
	}
	for _, id := range []string{"runner", "runner-1_2.example"} {
		if !validRunnerID(id) {
			t.Fatalf("validRunnerID(%q) = false", id)
		}
	}

	stateRoot := filepath.Join(t.TempDir(), "state")
	if err = os.WriteFile(stateRoot, []byte("not a directory"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err = failedConfigurationJob(
		NewStore(stateRoot),
		repositoryPath,
		"main",
		plumbing.ZeroHash,
		os.ErrInvalid,
	); err == nil {
		t.Fatal("failed configuration state error was ignored")
	}
	if _, err = os.Stat(root); err != nil {
		t.Fatal(err)
	}
}

func coordinatorRepository(
	t *testing.T,
) (string, repopath.Repository, storage.Store, *Coordinator) {
	t.Helper()
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
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Storage: repositoryStore,
		State:   NewStore(root),
	})
	if err != nil {
		t.Fatal(err)
	}
	return root, repositoryPath, repositoryStore, coordinator
}
