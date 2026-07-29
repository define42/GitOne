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

	"github.com/define42/GitOne/internal/repoconfig"
	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/storage"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
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
	storage  storage.Store
	state    Store
	executor Executor
	jobs     chan buildRequest
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

type buildRequest struct {
	repository repopath.Repository
	job        Job
	config     repoconfig.BuildConfig
}

func New(config Config) (*Runner, error) {
	if config.Executor == nil {
		return nil, errors.New("build executor is required")
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

// Schedule inspects .gitone.yaml at commit and queues a matching branch build.
// It returns nil when the commit has no build or its branch filter does not match.
func (r *Runner) Schedule(
	repositoryPath repopath.Repository,
	branch string,
	commit plumbing.Hash,
) (*Job, error) {
	gitPath, err := r.storage.GitPath(repositoryPath)
	if err != nil {
		return nil, err
	}
	repository, err := git.PlainOpen(gitPath)
	if err != nil {
		return nil, err
	}
	repositoryConfig, found, err := repoconfig.Read(repository, commit)
	if err != nil {
		return r.failedConfigurationJob(repositoryPath, branch, commit, err)
	}
	if !found || repositoryConfig.Build == nil {
		return nil, nil
	}
	build := *repositoryConfig.Build
	if err = build.Validate(); err != nil {
		return r.failedConfigurationJob(repositoryPath, branch, commit, err)
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
	if err = r.state.save(repositoryPath, job); err != nil {
		return nil, err
	}
	request := buildRequest{repository: repositoryPath, job: job, config: build}
	select {
	case r.jobs <- request:
		return &job, nil
	default:
		finished := time.Now().UTC()
		job.Status = StatusFailed
		job.FinishedAt = &finished
		job.Error = "build queue is full"
		if saveErr := r.state.save(repositoryPath, job); saveErr != nil {
			return nil, errors.Join(errors.New(job.Error), saveErr)
		}
		return &job, errors.New(job.Error)
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
	if err := r.state.save(request.repository, job); err != nil {
		return
	}
	logFile, err := r.state.createLog(request.repository, job.ID)
	if err != nil {
		r.finish(request.repository, job, err)
		return
	}
	defer func() {
		_ = logFile.Close()
	}()
	buildLog := newCappedLogWriter(logFile, MaximumStoredLogBytes)
	if _, err = fmt.Fprintf(
		buildLog,
		"GitOne build %s\nrepository: %s\nbranch: %s\ncommit: %s\nimage: %s\n\n",
		job.ID,
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
	repository, err := git.PlainClone(destination, false, &git.CloneOptions{
		URL:        gitPath,
		NoCheckout: true,
	})
	if err != nil {
		return fmt.Errorf("prepare build checkout: %w", err)
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
	finished := time.Now().UTC()
	job.FinishedAt = &finished
	if buildErr == nil {
		job.Status = StatusSucceeded
	} else {
		job.Status = StatusFailed
		job.Error = buildErr.Error()
	}
	_ = r.state.save(repository, job)
}

func (r *Runner) failedConfigurationJob(
	repository repopath.Repository,
	branch string,
	commit plumbing.Hash,
	configurationErr error,
) (*Job, error) {
	return failedConfigurationJob(r.state, repository, branch, commit, configurationErr)
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
