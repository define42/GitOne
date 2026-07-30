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

const defaultLeaseDuration = 30 * time.Second

type Scheduler interface {
	Schedule(repopath.Repository, string, plumbing.Hash) (*Job, error)
}

// LockedScheduler lets callers that already hold the repository operations
// lock schedule a build without recursively acquiring that resource lock.
type LockedScheduler interface {
	Scheduler
	ScheduleLocked(repopath.Repository, string, plumbing.Hash) (*Job, error)
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
	Job          Job                    `json:"job"`
	Config       repoconfig.BuildConfig `json:"config"`
	LogOffset    int64                  `json:"logOffset"`
	LeaseSeconds int                    `json:"leaseSeconds"`
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

// Schedule persists a queued build for a remote runner to claim.
func (c *Coordinator) Schedule(
	repositoryPath repopath.Repository,
	branch string,
	commit plumbing.Hash,
) (*Job, error) {
	releaseOperation, err := acquireBuildOperationLock(c.storage.Root, repositoryPath)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = releaseOperation()
	}()
	return c.ScheduleLocked(repositoryPath, branch, commit)
}

// ScheduleLocked persists a queued build while its caller holds the repository
// operations lock.
func (c *Coordinator) ScheduleLocked(
	repositoryPath repopath.Repository,
	branch string,
	commit plumbing.Hash,
) (*Job, error) {
	repository, err := openRepositoryForBuild(c.storage, repositoryPath)
	if err != nil {
		return nil, err
	}
	repositoryConfig, found, err := repoconfig.Read(repository, commit)
	if err != nil {
		return failedConfigurationJob(c.state, repositoryPath, branch, commit, err)
	}
	if !found || repositoryConfig.Build == nil {
		return nil, nil
	}
	build := *repositoryConfig.Build
	if err = build.Validate(); err != nil {
		return failedConfigurationJob(c.state, repositoryPath, branch, commit, err)
	}
	if !build.MatchesBranch(branch) {
		return nil, nil
	}
	job := Job{
		ID:         newJobID(),
		Repository: repositoryPath.Full(),
		Branch:     branch,
		Commit:     commit.String(),
		Image:      build.Image,
		Status:     StatusQueued,
		CreatedAt:  time.Now().UTC(),
	}
	if err = c.state.save(repositoryPath, job); err != nil {
		return nil, err
	}
	return &job, nil
}

func (c *Coordinator) Claim(runnerID string) (*Lease, error) {
	if !validRunnerID(runnerID) {
		return nil, errors.New("invalid runner ID")
	}
	// Hold the queue lock for the whole claim so runners serialize against one
	// another, but scan the catalog under a shared lock. The scan walks every
	// group and re-reads every repository's build directory, and the catalog
	// lock is also taken shared by git receive-pack/upload-pack, browsing, and
	// LFS; taking it exclusive here stalled all of those behind the scan on
	// every runner poll. Each selected job is then mutated under a brief
	// exclusive catalog lock, which serializes the write against heartbeat and
	// complete (they hold the catalog lock shared) exactly as before.
	releaseQueue, err := lockmgr.Process.Acquire(lockmgr.QueueRequest(c.storage.Root))
	if err != nil {
		return nil, err
	}
	defer releaseQueue()

	candidates, err := c.scanCandidates(time.Now().UTC())
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		lease, claimed, claimErr := c.claimCandidate(candidate, runnerID)
		if claimErr != nil {
			return nil, claimErr
		}
		if claimed {
			return lease, nil
		}
	}
	return nil, nil
}

// scanCandidates lists claimable jobs across the catalog under a shared lock so
// the potentially long scan does not block concurrent repository operations.
func (c *Coordinator) scanCandidates(now time.Time) ([]jobCandidate, error) {
	release, err := lockmgr.Process.Acquire(
		lockmgr.CatalogRequest(c.storage.Root, lockmgr.Shared),
	)
	if err != nil {
		return nil, err
	}
	defer release()
	return c.candidates(now)
}

// claimCandidate re-reads a scanned job under an exclusive catalog lock and, if
// it is still claimable, leases it to the runner. Re-reading is required because
// the shared scan may have raced with a heartbeat or completion that renewed or
// finished the job. It returns claimed=false (without error) when the job is no
// longer claimable or its build configuration is invalid, so the caller keeps
// scanning the remaining candidates.
func (c *Coordinator) claimCandidate(candidate jobCandidate, runnerID string) (*Lease, bool, error) {
	release, err := lockmgr.Process.Acquire(
		lockmgr.CatalogRequest(c.storage.Root, lockmgr.Exclusive),
	)
	if err != nil {
		return nil, false, err
	}
	defer release()

	now := time.Now().UTC()
	job, err := c.state.Get(candidate.repository, candidate.job.ID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if !claimable(job, now) {
		return nil, false, nil
	}

	config, configErr := c.buildConfig(candidate.repository, job)
	if configErr != nil {
		finished := time.Now().UTC()
		job.Status = StatusFailed
		job.FinishedAt = &finished
		job.Error = "prepare remote build: " + configErr.Error()
		job.LeaseExpiresAt = nil
		if saveErr := c.state.save(candidate.repository, job); saveErr != nil {
			return nil, false, errors.Join(configErr, saveErr)
		}
		return nil, false, nil
	}

	leaseExpires := now.Add(c.leaseDuration)
	job.Repository = candidate.repository.Full()
	job.Status = StatusRunning
	if job.StartedAt == nil {
		job.StartedAt = &now
	}
	job.RunnerID = runnerID
	job.Attempt++
	job.LeaseExpiresAt = &leaseExpires
	if err = c.state.save(candidate.repository, job); err != nil {
		return nil, false, err
	}
	offset, err := c.state.logSize(candidate.repository, job.ID)
	if err != nil {
		return nil, false, err
	}
	return &Lease{
		Job:          job,
		Config:       config,
		LogOffset:    offset,
		LeaseSeconds: int(c.leaseDuration / time.Second),
	}, true, nil
}

func (c *Coordinator) Heartbeat(
	repository repopath.Repository,
	id string,
	runnerID string,
) (time.Time, error) {
	releaseOperation, _, err := acquireRepositoryBuildLock(c.storage, repository, id)
	if err != nil {
		return time.Time{}, err
	}
	defer func() {
		_ = releaseOperation()
	}()
	job, err := c.ownedRunningJob(repository, id, runnerID)
	if err != nil {
		return time.Time{}, err
	}
	expires := time.Now().UTC().Add(c.leaseDuration)
	job.LeaseExpiresAt = &expires
	if err = c.state.save(repository, job); err != nil {
		return time.Time{}, err
	}
	return expires, nil
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
	job, err := c.ownedRunningJob(repository, id, runnerID)
	if err != nil {
		return Job{}, err
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
	return job, nil
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

// claimable reports whether a job is available for a runner to lease: it is
// queued, or it is running with an expired (or absent) lease.
func claimable(job Job, now time.Time) bool {
	return job.Status == StatusQueued ||
		(job.Status == StatusRunning &&
			(job.LeaseExpiresAt == nil || !job.LeaseExpiresAt.After(now)))
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
			for _, job := range jobs {
				if claimable(job, now) {
					candidates = append(candidates, jobCandidate{
						repository: repository,
						job:        job,
					})
				}
			}
		}
	}
	sort.Slice(candidates, func(left, right int) bool {
		return candidates[left].job.CreatedAt.Before(candidates[right].job.CreatedAt)
	})
	return candidates, nil
}

func (c *Coordinator) buildConfig(
	repositoryPath repopath.Repository,
	job Job,
) (repoconfig.BuildConfig, error) {
	gitPath, err := c.storage.GitPath(repositoryPath)
	if err != nil {
		return repoconfig.BuildConfig{}, err
	}
	repository, err := git.PlainOpen(gitPath)
	if err != nil {
		return repoconfig.BuildConfig{}, err
	}
	config, found, err := repoconfig.Read(repository, plumbing.NewHash(job.Commit))
	if err != nil {
		return repoconfig.BuildConfig{}, err
	}
	if !found || config.Build == nil {
		return repoconfig.BuildConfig{}, errors.New("build configuration is missing")
	}
	if err = config.Build.Validate(); err != nil {
		return repoconfig.BuildConfig{}, err
	}
	return *config.Build, nil
}

func (c *Coordinator) ownedRunningJob(
	repository repopath.Repository,
	id string,
	runnerID string,
) (Job, error) {
	if !validRunnerID(runnerID) {
		return Job{}, errors.New("invalid runner ID")
	}
	job, err := c.state.Get(repository, id)
	if errors.Is(err, os.ErrNotExist) {
		return Job{}, errors.New("build not found")
	}
	if err != nil {
		return Job{}, err
	}
	if job.Status != StatusRunning {
		return Job{}, errors.New("build is not running")
	}
	if job.RunnerID != runnerID {
		return Job{}, errors.New("build is leased by another runner")
	}
	return job, nil
}

func validRunnerID(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > 100 {
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
		Repository: repository.Full(),
		Branch:     branch,
		Commit:     commit.String(),
		Status:     StatusFailed,
		CreatedAt:  finished,
		FinishedAt: &finished,
		Error:      "invalid " + repoconfig.FileName + " build: " + configurationErr.Error(),
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
