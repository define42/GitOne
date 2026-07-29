package runner

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

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

type CoordinatorConfig struct {
	Storage       storage.Store
	State         Store
	LeaseDuration time.Duration
}

type Coordinator struct {
	storage       storage.Store
	state         Store
	leaseDuration time.Duration
	mu            sync.Mutex
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
	gitPath, err := c.storage.GitPath(repositoryPath)
	if err != nil {
		return nil, err
	}
	repository, err := git.PlainOpen(gitPath)
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
	c.mu.Lock()
	defer c.mu.Unlock()

	candidates, err := c.candidates(time.Now().UTC())
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		config, configErr := c.buildConfig(candidate.repository, candidate.job)
		if configErr != nil {
			finished := time.Now().UTC()
			candidate.job.Status = StatusFailed
			candidate.job.FinishedAt = &finished
			candidate.job.Error = "prepare remote build: " + configErr.Error()
			candidate.job.LeaseExpiresAt = nil
			if saveErr := c.state.save(candidate.repository, candidate.job); saveErr != nil {
				return nil, errors.Join(configErr, saveErr)
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
	c.mu.Lock()
	defer c.mu.Unlock()
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
	c.mu.Lock()
	defer c.mu.Unlock()
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
	c.mu.Lock()
	defer c.mu.Unlock()
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
	c.mu.Lock()
	defer c.mu.Unlock()
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
