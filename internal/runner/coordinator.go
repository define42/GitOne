package runner

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/define42/GitOne/internal/lockmgr"
	"github.com/define42/GitOne/internal/repoconfig"
	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/storage"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

const (
	defaultLeaseDuration  = 30 * time.Second
	maximumRunnerIDLength = 100
)

var (
	ErrBuildNotFound          = errors.New("build not found")
	ErrBuildNotStartable      = errors.New("build is not waiting for manual start")
	ErrBuildNotRerunnable     = errors.New("only a completed build can be rerun")
	ErrBuildNotCancelable     = errors.New("completed build cannot be canceled")
	ErrBuildDependenciesUnmet = errors.New("build dependencies have not succeeded")
)

type Scheduler interface {
	Schedule(repopath.Repository, string, plumbing.Hash) ([]Job, error)
}

// LockedScheduler lets callers that already hold the repository operations
// lock schedule a build without recursively acquiring that resource lock.
type LockedScheduler interface {
	Scheduler
	ScheduleLocked(repopath.Repository, string, plumbing.Hash) ([]Job, error)
}

type CoordinatorConfig struct {
	Storage       storage.Store
	State         Store
	LeaseDuration time.Duration
}

type Coordinator struct {
	storage       storage.Store
	state         Store
	leaseDuration time.Duration
}

type Lease struct {
	Job          Job                  `json:"job"`
	Config       repoconfig.JobConfig `json:"config"`
	LogOffset    int64                `json:"logOffset"`
	LeaseSeconds int                  `json:"leaseSeconds"`
}

type HeartbeatResult struct {
	LeaseExpiresAt time.Time `json:"leaseExpiresAt"`
	Canceled       bool      `json:"canceled,omitempty"`
}

func NewCoordinator(config CoordinatorConfig) (*Coordinator, error) {
	if config.Storage.Root == "" {
		return nil, errors.New("repository storage root is required")
	}
	if config.State.Root == "" {
		return nil, errors.New("build state root is required")
	}
	if config.LeaseDuration == 0 {
		config.LeaseDuration = defaultLeaseDuration
	}
	if config.LeaseDuration < 5*time.Second || config.LeaseDuration > 10*time.Minute {
		return nil, errors.New("runner lease duration must be between 5 seconds and 10 minutes")
	}
	return &Coordinator{
		storage:       config.Storage,
		state:         config.State,
		leaseDuration: config.LeaseDuration,
	}, nil
}

func (c *Coordinator) Store() Store {
	return c.state
}

// Schedule persists every matching named job for remote runners to claim.
func (c *Coordinator) Schedule(
	repositoryPath repopath.Repository,
	branch string,
	commit plumbing.Hash,
) ([]Job, error) {
	releaseOperation, err := acquireBuildOperationLock(c.storage.Root, repositoryPath)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = releaseOperation()
	}()
	return c.ScheduleLocked(repositoryPath, branch, commit)
}

// ScheduleLocked persists matching named jobs while its caller holds the
// repository operations lock.
func (c *Coordinator) ScheduleLocked(
	repositoryPath repopath.Repository,
	branch string,
	commit plumbing.Hash,
) ([]Job, error) {
	repository, err := openRepositoryForBuild(c.storage, repositoryPath)
	if err != nil {
		return nil, err
	}
	repositoryConfig, found, err := repoconfig.Read(repository, commit)
	if err != nil {
		return failedConfigurationJobs(c.state, repositoryPath, branch, commit, err)
	}
	if !found || len(repositoryConfig.Jobs) == 0 {
		return []Job{}, nil
	}
	if err = repositoryConfig.Validate(); err != nil {
		return failedConfigurationJobs(c.state, repositoryPath, branch, commit, err)
	}
	jobs := configuredJobs(
		repositoryPath,
		branch,
		commit.String(),
		repositoryConfig,
		time.Now().UTC(),
	)
	for _, job := range jobs {
		if err = c.state.save(repositoryPath, job); err != nil {
			return nil, err
		}
	}
	if err = c.reconcileDependencies(repositoryPath); err != nil {
		return nil, err
	}
	for index := range jobs {
		stored, getErr := c.state.Get(repositoryPath, jobs[index].ID)
		if getErr == nil {
			jobs[index] = stored
		}
	}
	return jobs, nil
}

// Start moves a manually gated build into the runner queue.
func (c *Coordinator) Start(
	repositoryPath repopath.Repository,
	id string,
) (Job, error) {
	releaseOperation, _, err := acquireRepositoryBuildLock(
		c.storage,
		repositoryPath,
		id,
	)
	if err != nil {
		return Job{}, err
	}
	defer func() {
		_ = releaseOperation()
	}()
	job, err := c.state.Get(repositoryPath, id)
	if errors.Is(err, os.ErrNotExist) {
		return Job{}, ErrBuildNotFound
	}
	if err != nil {
		return Job{}, err
	}
	if job.Status != StatusManual {
		return Job{}, ErrBuildNotStartable
	}
	build, err := c.jobConfig(repositoryPath, job)
	if err != nil {
		return Job{}, fmt.Errorf("prepare manual build: %w", err)
	}
	if !build.Manual {
		return Job{}, errors.New("build configuration is not manual")
	}
	jobs, err := c.state.List(repositoryPath)
	if err != nil {
		return Job{}, err
	}
	ready, dependencyErr := jobDependenciesReady(job, jobs)
	if dependencyErr != nil {
		return Job{}, fmt.Errorf("%w: %w", ErrBuildDependenciesUnmet, dependencyErr)
	}
	if !ready {
		return Job{}, ErrBuildDependenciesUnmet
	}
	job.Status = StatusQueued
	if err = c.state.save(repositoryPath, job); err != nil {
		return Job{}, err
	}
	if err = c.reconcileDependencies(repositoryPath); err != nil {
		return Job{}, err
	}
	return job, nil
}

// Rerun creates a new queued build for the exact commit and branch recorded by
// a completed build. The original build and its log remain unchanged.
func (c *Coordinator) Rerun(
	repositoryPath repopath.Repository,
	id string,
) (Job, error) {
	releaseOperation, _, err := acquireRepositoryBuildLock(
		c.storage,
		repositoryPath,
		id,
	)
	if err != nil {
		return Job{}, err
	}
	defer func() {
		_ = releaseOperation()
	}()
	original, err := c.state.Get(repositoryPath, id)
	if errors.Is(err, os.ErrNotExist) {
		return Job{}, ErrBuildNotFound
	}
	if err != nil {
		return Job{}, err
	}
	if !terminalStatus(original.Status) {
		return Job{}, ErrBuildNotRerunnable
	}
	build, err := c.jobConfig(repositoryPath, original)
	if err != nil {
		return Job{}, fmt.Errorf("prepare build rerun: %w", err)
	}
	jobs, err := c.state.List(repositoryPath)
	if err != nil {
		return Job{}, err
	}
	dependencies, dependencyErr := successfulDependencies(build.Needs, original, jobs)
	if dependencyErr != nil {
		return Job{}, fmt.Errorf("%w: %w", ErrBuildDependenciesUnmet, dependencyErr)
	}
	job := Job{
		ID:         newJobID(),
		Name:       original.Name,
		Repository: repositoryPath.Full(),
		Branch:     original.Branch,
		Commit:     original.Commit,
		Image:      build.Image,
		Needs:      dependencies,
		Status:     StatusQueued,
		CreatedAt:  time.Now().UTC(),
		RerunOf:    original.ID,
	}
	if err = c.state.save(repositoryPath, job); err != nil {
		return Job{}, err
	}
	return job, nil
}

// Cancel moves a waiting, queued, or running build to a terminal canceled
// state. A running remote executor observes cancellation on its next heartbeat.
func (c *Coordinator) Cancel(
	repositoryPath repopath.Repository,
	id string,
) (Job, error) {
	releaseOperation, _, err := acquireRepositoryBuildLock(
		c.storage,
		repositoryPath,
		id,
	)
	if err != nil {
		return Job{}, err
	}
	defer func() {
		_ = releaseOperation()
	}()
	job, err := c.state.Get(repositoryPath, id)
	if errors.Is(err, os.ErrNotExist) {
		return Job{}, ErrBuildNotFound
	}
	if err != nil {
		return Job{}, err
	}
	if job.Status == StatusCanceled {
		return job, nil
	}
	if job.Status != StatusWaiting && job.Status != StatusQueued &&
		job.Status != StatusRunning {
		return Job{}, ErrBuildNotCancelable
	}
	finished := time.Now().UTC()
	job.Status = StatusCanceled
	job.FinishedAt = &finished
	job.LeaseExpiresAt = nil
	job.Error = ""
	if err = c.state.save(repositoryPath, job); err != nil {
		return Job{}, err
	}
	if err = c.reconcileDependencies(repositoryPath); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (c *Coordinator) Claim(runnerID string) (*Lease, error) {
	if !validRunnerID(runnerID) {
		return nil, errors.New("invalid runner ID")
	}
	releaseOperation, err := lockmgr.Process.Acquire(
		lockmgr.CatalogRequest(c.storage.Root, lockmgr.Exclusive),
		lockmgr.QueueRequest(c.storage.Root),
	)
	if err != nil {
		return nil, err
	}
	defer func() {
		releaseOperation()
	}()
	candidates, err := c.candidates(time.Now().UTC())
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		config, configErr := c.jobConfig(candidate.repository, candidate.job)
		if configErr != nil {
			finished := time.Now().UTC()
			candidate.job.Status = StatusFailed
			candidate.job.FinishedAt = &finished
			candidate.job.Error = "prepare remote build: " + configErr.Error()
			candidate.job.LeaseExpiresAt = nil
			if saveErr := c.state.save(candidate.repository, candidate.job); saveErr != nil {
				return nil, errors.Join(configErr, saveErr)
			}
			if reconcileErr := c.reconcileDependencies(candidate.repository); reconcileErr != nil {
				return nil, reconcileErr
			}
			continue
		}

		now := time.Now().UTC()
		leaseExpires := now.Add(c.leaseDuration)
		candidate.job.Repository = candidate.repository.Full()
		candidate.job.Status = StatusRunning
		if candidate.job.StartedAt == nil {
			candidate.job.StartedAt = &now
		}
		candidate.job.RunnerID = runnerID
		candidate.job.Attempt++
		candidate.job.LeaseExpiresAt = &leaseExpires
		if err = c.state.save(candidate.repository, candidate.job); err != nil {
			return nil, err
		}
		offset, err := c.state.logSize(candidate.repository, candidate.job.ID)
		if err != nil {
			return nil, err
		}
		return &Lease{
			Job:          candidate.job,
			Config:       config,
			LogOffset:    offset,
			LeaseSeconds: int(c.leaseDuration / time.Second),
		}, nil
	}
	return nil, nil
}

func (c *Coordinator) Heartbeat(
	repository repopath.Repository,
	id string,
	runnerID string,
) (time.Time, error) {
	result, err := c.HeartbeatState(repository, id, runnerID)
	return result.LeaseExpiresAt, err
}

// HeartbeatState extends a running lease and tells a remote runner when the
// build was canceled while it was executing.
func (c *Coordinator) HeartbeatState(
	repository repopath.Repository,
	id string,
	runnerID string,
) (HeartbeatResult, error) {
	releaseOperation, _, err := acquireRepositoryBuildLock(c.storage, repository, id)
	if err != nil {
		return HeartbeatResult{}, err
	}
	defer func() {
		_ = releaseOperation()
	}()
	job, err := c.ownedJob(repository, id, runnerID)
	if err != nil {
		return HeartbeatResult{}, err
	}
	if terminalStatus(job.Status) {
		if job.FinishedAt == nil {
			return HeartbeatResult{}, errors.New("completed build has no finish time")
		}
		return HeartbeatResult{
			LeaseExpiresAt: *job.FinishedAt,
			Canceled:       job.Status == StatusCanceled,
		}, nil
	}
	if job.Status != StatusRunning {
		return HeartbeatResult{}, errors.New("build is not running")
	}
	expires := time.Now().UTC().Add(c.leaseDuration)
	job.LeaseExpiresAt = &expires
	if err = c.state.save(repository, job); err != nil {
		return HeartbeatResult{}, err
	}
	return HeartbeatResult{LeaseExpiresAt: expires}, nil
}

func (c *Coordinator) AppendLog(
	repository repopath.Repository,
	id string,
	runnerID string,
	offset int64,
	contents []byte,
) (int64, error) {
	releaseOperation, _, err := acquireRepositoryBuildLock(c.storage, repository, id)
	if err != nil {
		return 0, err
	}
	defer func() {
		_ = releaseOperation()
	}()
	job, err := c.ownedRunningJob(repository, id, runnerID)
	if err != nil {
		return 0, err
	}
	nextOffset, err := c.state.appendLog(repository, id, offset, contents)
	if err != nil {
		return 0, err
	}
	expires := time.Now().UTC().Add(c.leaseDuration)
	job.LeaseExpiresAt = &expires
	if err = c.state.save(repository, job); err != nil {
		return 0, err
	}
	return nextOffset, nil
}

func (c *Coordinator) Complete(
	repository repopath.Repository,
	id string,
	runnerID string,
	buildError string,
) (Job, error) {
	releaseOperation, _, err := acquireRepositoryBuildLock(c.storage, repository, id)
	if err != nil {
		return Job{}, err
	}
	defer func() {
		_ = releaseOperation()
	}()
	job, err := c.ownedJob(repository, id, runnerID)
	if err != nil {
		return Job{}, err
	}
	expectedStatus := StatusSucceeded
	if buildError != "" {
		expectedStatus = StatusFailed
	}
	if job.Status == StatusCanceled {
		return job, nil
	}
	if job.Status == StatusSucceeded || job.Status == StatusFailed {
		if job.Status == expectedStatus && job.Error == buildError {
			return job, nil
		}
		return Job{}, errors.New("build was already completed with a different result")
	}
	if job.Status != StatusRunning {
		return Job{}, errors.New("build is not running")
	}
	finished := time.Now().UTC()
	job.FinishedAt = &finished
	job.LeaseExpiresAt = nil
	if buildError == "" {
		job.Status = StatusSucceeded
		job.Error = ""
	} else {
		job.Status = StatusFailed
		job.Error = buildError
	}
	if err = c.state.save(repository, job); err != nil {
		return Job{}, err
	}
	if err = c.reconcileDependencies(repository); err != nil {
		return Job{}, err
	}
	return job, nil
}

func terminalStatus(status Status) bool {
	return status == StatusSucceeded || status == StatusFailed || status == StatusCanceled
}

func (c *Coordinator) SourceJob(
	repository repopath.Repository,
	id string,
	runnerID string,
) (Job, error) {
	releaseOperation, _, err := acquireRepositoryBuildLock(c.storage, repository, id)
	if err != nil {
		return Job{}, err
	}
	defer func() {
		_ = releaseOperation()
	}()
	return c.ownedRunningJob(repository, id, runnerID)
}

type jobCandidate struct {
	repository repopath.Repository
	job        Job
}

func (c *Coordinator) candidates(now time.Time) ([]jobCandidate, error) {
	groups, err := c.storage.ListGroups()
	if err != nil {
		return nil, err
	}
	candidates := make([]jobCandidate, 0)
	for _, group := range groups {
		groupParts, parseErr := repopath.ParseGroup(group.Path)
		if parseErr != nil {
			return nil, parseErr
		}
		for _, name := range group.Repositories {
			repository := repopath.Repository{Groups: groupParts, Name: name}
			jobs, listErr := c.state.List(repository)
			if listErr != nil {
				return nil, listErr
			}
			updates, _ := reconcileJobDependencies(jobs, now)
			for _, update := range updates {
				if saveErr := c.state.save(repository, update); saveErr != nil {
					return nil, saveErr
				}
			}
			for _, job := range jobs {
				if job.Status == StatusQueued ||
					(job.Status == StatusRunning &&
						(job.LeaseExpiresAt == nil || !job.LeaseExpiresAt.After(now))) {
					candidates = append(candidates, jobCandidate{
						repository: repository,
						job:        job,
					})
				}
			}
		}
	}
	sort.Slice(candidates, func(left, right int) bool {
		if candidates[left].job.CreatedAt.Equal(candidates[right].job.CreatedAt) {
			leftRepository := candidates[left].repository.Full()
			rightRepository := candidates[right].repository.Full()
			if leftRepository == rightRepository {
				return candidates[left].job.Name < candidates[right].job.Name
			}
			return leftRepository < rightRepository
		}
		return candidates[left].job.CreatedAt.Before(candidates[right].job.CreatedAt)
	})
	return candidates, nil
}

func (c *Coordinator) reconcileDependencies(repository repopath.Repository) error {
	jobs, err := c.state.List(repository)
	if err != nil {
		return err
	}
	updates, _ := reconcileJobDependencies(jobs, time.Now().UTC())
	for _, update := range updates {
		if err = c.state.save(repository, update); err != nil {
			return err
		}
	}
	return nil
}

func (c *Coordinator) jobConfig(
	repositoryPath repopath.Repository,
	job Job,
) (repoconfig.JobConfig, error) {
	gitPath, err := c.storage.GitPath(repositoryPath)
	if err != nil {
		return repoconfig.JobConfig{}, err
	}
	repository, err := git.PlainOpen(gitPath)
	if err != nil {
		return repoconfig.JobConfig{}, err
	}
	config, found, err := repoconfig.Read(repository, plumbing.NewHash(job.Commit))
	if err != nil {
		return repoconfig.JobConfig{}, err
	}
	if !found {
		return repoconfig.JobConfig{}, errors.New("job configuration is missing")
	}
	if err = config.Validate(); err != nil {
		return repoconfig.JobConfig{}, err
	}
	build, found := config.Jobs[job.Name]
	if !found {
		return repoconfig.JobConfig{}, fmt.Errorf("job %q is missing", job.Name)
	}
	return build, nil
}

func (c *Coordinator) ownedRunningJob(
	repository repopath.Repository,
	id string,
	runnerID string,
) (Job, error) {
	job, err := c.ownedJob(repository, id, runnerID)
	if err != nil {
		return Job{}, err
	}
	if job.Status != StatusRunning {
		return Job{}, errors.New("build is not running")
	}
	return job, nil
}

func (c *Coordinator) ownedJob(
	repository repopath.Repository,
	id string,
	runnerID string,
) (Job, error) {
	if !validRunnerID(runnerID) {
		return Job{}, errors.New("invalid runner ID")
	}
	job, err := c.state.Get(repository, id)
	if errors.Is(err, os.ErrNotExist) {
		return Job{}, ErrBuildNotFound
	}
	if err != nil {
		return Job{}, err
	}
	if job.RunnerID != runnerID {
		return Job{}, errors.New("build is leased by another runner")
	}
	return job, nil
}

func validRunnerID(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > maximumRunnerIDLength {
		return false
	}
	for _, character := range id {
		if !((character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '-' || character == '_' || character == '.') {
			return false
		}
	}
	return true
}

func failedConfigurationJob(
	state Store,
	repository repopath.Repository,
	branch string,
	commit plumbing.Hash,
	configurationErr error,
) (*Job, error) {
	finished := time.Now().UTC()
	job := Job{
		ID:         newJobID(),
		Name:       "configuration",
		Repository: repository.Full(),
		Branch:     branch,
		Commit:     commit.String(),
		Status:     StatusFailed,
		CreatedAt:  finished,
		FinishedAt: &finished,
		Error:      "invalid " + repoconfig.FileName + " jobs: " + configurationErr.Error(),
	}
	if err := state.save(repository, job); err != nil {
		return nil, errors.Join(configurationErr, err)
	}
	logFile, err := state.createLog(repository, job.ID)
	if err == nil {
		_, _ = fmt.Fprintln(logFile, job.Error)
		_ = logFile.Close()
	}
	return &job, nil
}

func failedConfigurationJobs(
	state Store,
	repository repopath.Repository,
	branch string,
	commit plumbing.Hash,
	configurationErr error,
) ([]Job, error) {
	job, err := failedConfigurationJob(state, repository, branch, commit, configurationErr)
	if job == nil {
		return nil, err
	}
	return []Job{*job}, err
}
