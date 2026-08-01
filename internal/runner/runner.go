package runner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/define42/GitOne/internal/gitformat"
	"github.com/define42/GitOne/internal/repoconfig"
	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/storage"
	"github.com/go-git/go-billy/v6/osfs"
	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/cache"
	formatcfg "github.com/go-git/go-git/v6/plumbing/format/config"
	gitfilesystem "github.com/go-git/go-git/v6/storage/filesystem"
)

type Config struct {
	Storage  storage.Store
	State    Store
	Executor Executor
	Workers  int
	Queue    int
}

const MaximumStoredLogBytes int64 = 10 << 20

const logTruncatedMarker = "\n[build log truncated by GitOne]\n"

type Runner struct {
	storage      storage.Store
	state        Store
	executor     Executor
	jobs         chan buildRequest
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	dependencyMu sync.Mutex
}

type buildRequest struct {
	repository repopath.Repository
	job        Job
	config     repoconfig.JobConfig
}

func New(config Config) (*Runner, error) {
	if config.Executor == nil {
		return nil, errors.New("build executor is required")
	}
	if config.Storage.Root == "" {
		return nil, errors.New("repository storage root is required")
	}
	if config.State.Root == "" {
		return nil, errors.New("build state root is required")
	}
	if config.Workers == 0 {
		config.Workers = 1
	}
	if config.Workers < 1 || config.Workers > 32 {
		return nil, errors.New("build workers must be between 1 and 32")
	}
	if config.Queue == 0 {
		config.Queue = 64
	}
	if config.Queue < config.Workers {
		return nil, errors.New("build queue must be at least as large as the worker count")
	}
	if err := os.MkdirAll(config.State.Root, 0o750); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	runner := &Runner{
		storage:  config.Storage,
		state:    config.State,
		executor: config.Executor,
		jobs:     make(chan buildRequest, config.Queue),
		cancel:   cancel,
	}
	for range config.Workers {
		runner.wg.Add(1)
		go runner.worker(ctx)
	}
	return runner, nil
}

func (r *Runner) Store() Store {
	return r.state
}

// Schedule inspects .gitone.yaml at commit and queues every matching named job.
// It returns an empty slice when no job matches the branch.
func (r *Runner) Schedule(
	repositoryPath repopath.Repository,
	branch string,
	commit plumbing.Hash,
) ([]Job, error) {
	releaseOperation, err := acquireBuildOperationLock(r.storage.Root, repositoryPath)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = releaseOperation()
	}()
	return r.ScheduleLocked(repositoryPath, branch, commit)
}

// ScheduleLocked queues matching jobs while its caller holds the repository
// operations lock.
func (r *Runner) ScheduleLocked(
	repositoryPath repopath.Repository,
	branch string,
	commit plumbing.Hash,
) ([]Job, error) {
	repository, err := openRepositoryForBuild(r.storage, repositoryPath)
	if err != nil {
		return nil, err
	}
	repositoryConfig, found, err := repoconfig.Read(repository, commit)
	if err != nil {
		return failedConfigurationJobs(r.state, repositoryPath, branch, commit, err)
	}
	if !found || len(repositoryConfig.Jobs) == 0 {
		return []Job{}, nil
	}
	if err = repositoryConfig.Validate(); err != nil {
		return failedConfigurationJobs(r.state, repositoryPath, branch, commit, err)
	}
	r.dependencyMu.Lock()
	defer r.dependencyMu.Unlock()
	jobs := configuredJobs(
		repositoryPath,
		branch,
		commit.String(),
		repositoryConfig,
		time.Now().UTC(),
	)
	var scheduleErr error
	for _, job := range jobs {
		if err = r.state.save(repositoryPath, job); err != nil {
			return jobs, errors.Join(scheduleErr, err)
		}
	}
	for index := range jobs {
		job := &jobs[index]
		if job.Status != StatusQueued {
			continue
		}
		build := repositoryConfig.Jobs[job.Name]
		request := buildRequest{repository: repositoryPath, job: *job, config: build}
		select {
		case r.jobs <- request:
		default:
			finished := time.Now().UTC()
			job.Status = StatusFailed
			job.FinishedAt = &finished
			job.Error = "build queue is full"
			if saveErr := r.state.save(repositoryPath, *job); saveErr != nil {
				return jobs, errors.Join(scheduleErr, errors.New(job.Error), saveErr)
			}
			scheduleErr = errors.Join(scheduleErr, errors.New(job.Error))
		}
	}
	if dependencyErr := r.advanceDependenciesLocked(repositoryPath); dependencyErr != nil {
		scheduleErr = errors.Join(scheduleErr, dependencyErr)
	}
	for index := range jobs {
		stored, getErr := r.state.Get(repositoryPath, jobs[index].ID)
		if getErr == nil {
			jobs[index] = stored
		}
	}
	return jobs, scheduleErr
}

// Start queues a manual build when using the in-process runner.
func (r *Runner) Start(
	repositoryPath repopath.Repository,
	id string,
) (Job, error) {
	releaseOperation, repository, err := acquireRepositoryBuildLock(
		r.storage,
		repositoryPath,
		id,
	)
	if err != nil {
		return Job{}, err
	}
	defer func() {
		_ = releaseOperation()
	}()
	r.dependencyMu.Lock()
	defer r.dependencyMu.Unlock()
	job, err := r.state.Get(repositoryPath, id)
	if errors.Is(err, os.ErrNotExist) {
		return Job{}, ErrBuildNotFound
	}
	if err != nil {
		return Job{}, err
	}
	if job.Status != StatusManual {
		return Job{}, ErrBuildNotStartable
	}
	config, found, err := repoconfig.Read(repository, plumbing.NewHash(job.Commit))
	if err != nil {
		return Job{}, fmt.Errorf("prepare manual build: %w", err)
	}
	if !found {
		return Job{}, errors.New("job configuration is missing")
	}
	if err = config.Validate(); err != nil {
		return Job{}, fmt.Errorf("prepare manual build: %w", err)
	}
	build, found := config.Jobs[job.Name]
	if !found {
		return Job{}, fmt.Errorf("job %q is missing", job.Name)
	}
	if !build.Manual {
		return Job{}, errors.New("job configuration is not manual")
	}
	jobs, err := r.state.List(repositoryPath)
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
	original := job
	job.Status = StatusQueued
	if err = r.state.save(repositoryPath, job); err != nil {
		return Job{}, err
	}
	request := buildRequest{repository: repositoryPath, job: job, config: build}
	select {
	case r.jobs <- request:
		return job, nil
	default:
		restoreErr := r.state.save(repositoryPath, original)
		return Job{}, errors.Join(errors.New("build queue is full"), restoreErr)
	}
}

func (r *Runner) Close() {
	r.cancel()
	r.wg.Wait()
}

func (r *Runner) worker(ctx context.Context) {
	defer r.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case request := <-r.jobs:
			r.run(ctx, request)
		}
	}
}

func (r *Runner) run(parent context.Context, request buildRequest) {
	job := request.job
	started := time.Now().UTC()
	job.Status = StatusRunning
	job.StartedAt = &started
	releaseOperation, _, err := acquireRepositoryBuildLock(
		r.storage,
		request.repository,
		job.ID,
	)
	if err != nil {
		return
	}
	if err = r.state.save(request.repository, job); err != nil {
		_ = releaseOperation()
		return
	}
	logFile, err := r.state.createLog(request.repository, job.ID)
	if err != nil {
		_ = releaseOperation()
		r.finish(request.repository, job, err)
		return
	}
	err = errors.Join(logFile.Close(), releaseOperation())
	if err != nil {
		r.finish(request.repository, job, err)
		return
	}
	buildLog := newCappedLogWriter(&operationBuildLogWriter{
		runner:     r,
		repository: request.repository,
		id:         job.ID,
	}, MaximumStoredLogBytes)
	if _, err = fmt.Fprintf(
		buildLog,
		"GitOne build %s\njob: %s\nneeds: %s\nrepository: %s\nbranch: %s\ncommit: %s\nimage: %s\n\n",
		job.ID,
		job.Name,
		dependencyNames(job),
		job.Repository,
		job.Branch,
		job.Commit,
		job.Image,
	); err != nil {
		r.finish(request.repository, job, fmt.Errorf("write build log: %w", err))
		return
	}

	workRoot, err := r.state.workRoot()
	if err == nil {
		err = os.MkdirAll(workRoot, 0o750)
	}
	var workspace string
	if err == nil {
		workspace, err = os.MkdirTemp(workRoot, "build-"+job.ID+"-")
	}
	if workspace != "" {
		defer func() {
			_ = os.RemoveAll(workspace)
		}()
	}
	if err == nil {
		err = r.checkout(request.repository, plumbing.NewHash(job.Commit), workspace, buildLog)
	}
	if err == nil {
		timeout := time.Duration(request.config.Timeout()) * time.Second
		buildContext, cancel := context.WithTimeout(parent, timeout)
		err = r.executor.Run(buildContext, ExecuteRequest{
			Job:       job,
			Directory: workspace,
			Config:    request.config,
		}, buildLog)
		if errors.Is(buildContext.Err(), context.DeadlineExceeded) {
			err = fmt.Errorf("build timed out after %s", timeout)
		}
		cancel()
	}
	r.finish(request.repository, job, err)
}

func (r *Runner) checkout(
	repositoryPath repopath.Repository,
	commit plumbing.Hash,
	destination string,
	output io.Writer,
) error {
	gitPath, err := r.storage.GitPath(repositoryPath)
	if err != nil {
		return err
	}
	worktreeFS := osfs.New(destination, osfs.WithBoundOS())
	gitDirectory, err := worktreeFS.Chroot(git.GitDirName)
	if err != nil {
		return fmt.Errorf("prepare build Git directory: %w", err)
	}
	checkoutStorage := gitfilesystem.NewStorageWithOptions(
		gitDirectory,
		cache.NewObjectLRUDefault(),
		gitfilesystem.Options{ObjectFormat: formatcfg.SHA256},
	)
	repository, err := git.Clone(checkoutStorage, worktreeFS, &git.CloneOptions{
		URL:        gitPath,
		NoCheckout: true,
	})
	if err != nil {
		if repository != nil {
			_ = repository.Close()
		} else {
			_ = checkoutStorage.Close()
		}
		return fmt.Errorf("prepare build checkout: %w", err)
	}
	defer func() { _ = repository.Close() }()
	if err = gitformat.Validate(repository); err != nil {
		return fmt.Errorf("validate build checkout: %w", err)
	}
	worktree, err := repository.Worktree()
	if err != nil {
		return fmt.Errorf("open build checkout: %w", err)
	}
	if err = worktree.Checkout(&git.CheckoutOptions{Hash: commit, Force: true}); err != nil {
		return fmt.Errorf("checkout build commit: %w", err)
	}
	if _, err = fmt.Fprintln(output, "checked out "+commit.String()); err != nil {
		return fmt.Errorf("write build log: %w", err)
	}
	return nil
}

func (r *Runner) finish(repository repopath.Repository, job Job, buildErr error) {
	releaseOperation, _, err := acquireRepositoryBuildLock(r.storage, repository, job.ID)
	if err != nil {
		return
	}
	finished := time.Now().UTC()
	job.FinishedAt = &finished
	if buildErr == nil {
		job.Status = StatusSucceeded
	} else {
		job.Status = StatusFailed
		job.Error = buildErr.Error()
	}
	saveErr := r.state.save(repository, job)
	if saveErr == nil {
		r.dependencyMu.Lock()
		_ = r.advanceDependenciesLocked(repository)
		r.dependencyMu.Unlock()
	}
	_ = releaseOperation()
}

func (r *Runner) advanceDependenciesLocked(repository repopath.Repository) error {
	for {
		jobs, err := r.state.List(repository)
		if err != nil {
			return err
		}
		updates, promoted := reconcileJobDependencies(jobs, time.Now().UTC())
		for _, update := range updates {
			if err = r.state.save(repository, update); err != nil {
				return err
			}
		}
		queueFailure := false
		for _, job := range promoted {
			config, configErr := r.jobConfig(repository, job)
			if configErr == nil {
				select {
				case r.jobs <- buildRequest{repository: repository, job: job, config: config}:
					continue
				default:
					configErr = errors.New("build queue is full")
				}
			}
			job.Status = StatusFailed
			job.FinishedAt = timePointer(time.Now().UTC())
			job.Error = configErr.Error()
			if err = r.state.save(repository, job); err != nil {
				return errors.Join(configErr, err)
			}
			queueFailure = true
		}
		if !queueFailure {
			return nil
		}
	}
}

func (r *Runner) jobConfig(
	repositoryPath repopath.Repository,
	job Job,
) (repoconfig.JobConfig, error) {
	repository, err := openRepositoryForBuild(r.storage, repositoryPath)
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
	jobConfig, found := config.Jobs[job.Name]
	if !found {
		return repoconfig.JobConfig{}, fmt.Errorf("job %q is missing", job.Name)
	}
	return jobConfig, nil
}

type operationBuildLogWriter struct {
	runner     *Runner
	repository repopath.Repository
	id         string
	offset     int64
}

func (w *operationBuildLogWriter) Write(contents []byte) (int, error) {
	releaseOperation, _, err := acquireRepositoryBuildLock(
		w.runner.storage,
		w.repository,
		w.id,
	)
	if err != nil {
		return 0, err
	}
	nextOffset, writeErr := w.runner.state.appendLog(
		w.repository,
		w.id,
		w.offset,
		contents,
	)
	releaseErr := releaseOperation()
	if writeErr != nil {
		return 0, errors.Join(writeErr, releaseErr)
	}
	written := nextOffset - w.offset
	w.offset = nextOffset
	if written != int64(len(contents)) {
		return int(written), errors.Join(io.ErrShortWrite, releaseErr)
	}
	return len(contents), releaseErr
}

func newJobID() string {
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		return time.Now().UTC().Format("20060102T150405000000000")
	}
	return time.Now().UTC().Format("20060102T150405000000000") + "-" + hex.EncodeToString(random)
}

type cappedLogWriter struct {
	writer    io.Writer
	remaining int64
	truncated bool
	mu        sync.Mutex
}

func newCappedLogWriter(writer io.Writer, maximum int64) *cappedLogWriter {
	remaining := maximum - int64(len(logTruncatedMarker))
	if remaining < 0 {
		remaining = 0
	}
	return &cappedLogWriter{writer: writer, remaining: remaining}
}

func (w *cappedLogWriter) Write(contents []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.truncated {
		return len(contents), nil
	}
	if int64(len(contents)) <= w.remaining {
		written, err := w.writer.Write(contents)
		w.remaining -= int64(written)
		return written, err
	}
	toWrite := int(w.remaining)
	if toWrite > 0 {
		written, err := w.writer.Write(contents[:toWrite])
		w.remaining -= int64(written)
		if err != nil {
			return written, err
		}
		if written != toWrite {
			return written, io.ErrShortWrite
		}
	}
	if _, err := io.WriteString(w.writer, logTruncatedMarker); err != nil {
		return toWrite, err
	}
	w.truncated = true
	return len(contents), nil
}
