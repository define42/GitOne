package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

type RemoteConfig struct {
	URL          string
	Token        string
	ID           string
	WorkRoot     string
	Workers      int
	PollInterval time.Duration
	HTTPClient   *http.Client
	Executor     Executor
}

type Remote struct {
	baseURL      *url.URL
	token        string
	id           string
	workRoot     string
	workers      int
	pollInterval time.Duration
	client       *http.Client
	executor     Executor
}

func NewRemote(config RemoteConfig) (*Remote, error) {
	baseURL, err := url.Parse(config.URL)
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" {
		return nil, errors.New("valid runner server URL is required")
	}
	if config.Token == "" {
		return nil, errors.New("runner token is required")
	}
	if !validRunnerID(config.ID) {
		return nil, errors.New("valid runner ID is required")
	}
	if config.WorkRoot == "" {
		return nil, errors.New("runner work root is required")
	}
	if config.Workers == 0 {
		config.Workers = 1
	}
	if config.Workers < 1 || config.Workers > 32 {
		return nil, errors.New("runner workers must be between 1 and 32")
	}
	if config.PollInterval == 0 {
		config.PollInterval = 2 * time.Second
	}
	if config.PollInterval < 250*time.Millisecond || config.PollInterval > time.Minute {
		return nil, errors.New("runner poll interval must be between 250ms and 1 minute")
	}
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{Timeout: 2 * time.Minute}
	}
	if config.Executor == nil {
		return nil, errors.New("remote build executor is required")
	}
	if err = os.MkdirAll(config.WorkRoot, 0o750); err != nil {
		return nil, err
	}
	return &Remote{
		baseURL:      baseURL,
		token:        config.Token,
		id:           config.ID,
		workRoot:     config.WorkRoot,
		workers:      config.Workers,
		pollInterval: config.PollInterval,
		client:       config.HTTPClient,
		executor:     config.Executor,
	}, nil
}

func (r *Remote) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errorsChannel := make(chan error, r.workers)
	var workers sync.WaitGroup
	for index := range r.workers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			runnerID := r.id
			if r.workers > 1 {
				runnerID += "-" + strconv.Itoa(index+1)
			}
			if err := r.worker(ctx, runnerID); err != nil {
				select {
				case errorsChannel <- err:
				default:
				}
				cancel()
			}
		}()
	}
	done := make(chan struct{})
	go func() {
		workers.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		<-done
		select {
		case err := <-errorsChannel:
			return err
		default:
			return context.Cause(ctx)
		}
	case err := <-errorsChannel:
		<-done
		return err
	}
}

func (r *Remote) worker(ctx context.Context, runnerID string) error {
	for {
		lease, err := r.claim(ctx, runnerID)
		if err != nil {
			var responseErr *remoteResponseError
			if errors.As(err, &responseErr) &&
				(responseErr.Status == http.StatusUnauthorized ||
					responseErr.Status == http.StatusForbidden) {
				return err
			}
			log.Printf("runner %s could not claim a build: %v", runnerID, err)
			if waitErr := waitForRemote(ctx, r.pollInterval); waitErr != nil {
				return waitErr
			}
			continue
		}
		if lease == nil {
			if err = waitForRemote(ctx, r.pollInterval); err != nil {
				return err
			}
			continue
		}
		if err = r.runLease(ctx, runnerID, *lease); err != nil {
			log.Printf("runner %s build %s: %v", runnerID, lease.Job.ID, err)
		}
	}
}

func (r *Remote) runLease(parent context.Context, runnerID string, lease Lease) error {
	workspace, err := os.MkdirTemp(r.workRoot, "build-"+lease.Job.ID+"-")
	if err != nil {
		return r.complete(parent, runnerID, lease.Job, err)
	}
	defer func() {
		_ = os.RemoveAll(workspace)
	}()

	buildContext, cancel := context.WithTimeout(
		parent,
		time.Duration(lease.Config.Timeout())*time.Second,
	)
	defer cancel()
	heartbeatErrors := make(chan error, 1)
	heartbeatDone := make(chan struct{})
	go r.heartbeat(
		buildContext,
		cancel,
		runnerID,
		lease,
		heartbeatErrors,
		heartbeatDone,
	)

	logWriter := &remoteLogWriter{
		remote:   r,
		ctx:      buildContext,
		runnerID: runnerID,
		job:      lease.Job,
		offset:   lease.LogOffset,
	}
	if lease.LogOffset > 0 {
		_, _ = fmt.Fprintf(
			logWriter,
			"\n[runner %s reclaimed build for attempt %d]\n\n",
			runnerID,
			lease.Job.Attempt,
		)
	}
	_, err = fmt.Fprintf(
		logWriter,
		"GitOne remote build %s\nrunner: %s\nrepository: %s\nbranch: %s\ncommit: %s\nimage: %s\n\n",
		lease.Job.ID,
		runnerID,
		lease.Job.Repository,
		lease.Job.Branch,
		lease.Job.Commit,
		lease.Job.Image,
	)
	if err == nil {
		err = r.downloadSource(buildContext, runnerID, lease.Job, workspace)
	}
	if err == nil {
		_, err = fmt.Fprintln(logWriter, "downloaded source at "+lease.Job.Commit)
	}
	if err == nil {
		err = r.executor.Run(buildContext, ExecuteRequest{
			Job:       lease.Job,
			Directory: workspace,
			Config:    lease.Config,
		}, logWriter)
	}
	if errors.Is(buildContext.Err(), context.DeadlineExceeded) {
		err = fmt.Errorf("build timed out after %s", time.Duration(lease.Config.Timeout())*time.Second)
	}
	cancel()
	<-heartbeatDone
	select {
	case heartbeatErr := <-heartbeatErrors:
		if err == nil {
			err = heartbeatErr
		}
	default:
	}
	return r.complete(parent, runnerID, lease.Job, err)
}

func (r *Remote) heartbeat(
	ctx context.Context,
	cancel context.CancelFunc,
	runnerID string,
	lease Lease,
	errorsChannel chan<- error,
	done chan<- struct{},
) {
	defer close(done)
	interval := time.Duration(lease.LeaseSeconds) * time.Second / 3
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			body := runnerJobRequest{
				RunnerID: runnerID, Repository: lease.Job.Repository, ID: lease.Job.ID,
			}
			if err := r.post(ctx, "/api/runner/jobs/heartbeat", body, nil); err != nil {
				select {
				case errorsChannel <- fmt.Errorf("heartbeat build: %w", err):
				default:
				}
				cancel()
				return
			}
		}
	}
}

func (r *Remote) claim(ctx context.Context, runnerID string) (*Lease, error) {
	var response struct {
		Lease *Lease `json:"lease"`
	}
	if err := r.post(
		ctx,
		"/api/runner/jobs/claim",
		map[string]string{"runnerId": runnerID},
		&response,
	); err != nil {
		return nil, err
	}
	return response.Lease, nil
}

func (r *Remote) downloadSource(
	ctx context.Context,
	runnerID string,
	job Job,
	destination string,
) error {
	endpoint := *r.baseURL
	endpoint.Path = path.Join(endpoint.Path, "/api/runner/source")
	query := endpoint.Query()
	query.Set("repository", job.Repository)
	query.Set("id", job.ID)
	query.Set("runnerId", runnerID)
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+r.token)
	response, err := r.client.Do(request)
	if err != nil {
		return err
	}
	defer func() {
		_ = response.Body.Close()
	}()
	if response.StatusCode != http.StatusOK {
		return readRemoteResponseError(response)
	}
	return ExtractSourceArchive(response.Body, destination)
}

func (r *Remote) appendLog(
	ctx context.Context,
	runnerID string,
	job Job,
	offset int64,
	contents []byte,
) (int64, error) {
	var response struct {
		Offset int64 `json:"offset"`
	}
	err := r.post(ctx, "/api/runner/jobs/log", runnerLogRequest{
		RunnerID: runnerID, Repository: job.Repository, ID: job.ID,
		Offset: offset, Content: contents,
	}, &response)
	return response.Offset, err
}

func (r *Remote) complete(
	ctx context.Context,
	runnerID string,
	job Job,
	buildErr error,
) error {
	errorMessage := ""
	if buildErr != nil {
		errorMessage = buildErr.Error()
	}
	request := runnerCompleteRequest{
		RunnerID: runnerID, Repository: job.Repository, ID: job.ID, Error: errorMessage,
	}
	if err := r.post(ctx, "/api/runner/jobs/complete", request, nil); err != nil {
		return fmt.Errorf("complete build: %w", err)
	}
	return buildErr
}

func (r *Remote) post(ctx context.Context, endpoint string, body any, output any) error {
	contents, err := json.Marshal(body)
	if err != nil {
		return err
	}
	target := *r.baseURL
	target.Path = path.Join(target.Path, endpoint)
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		target.String(),
		bytes.NewReader(contents),
	)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+r.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := r.client.Do(request)
	if err != nil {
		return err
	}
	defer func() {
		_ = response.Body.Close()
	}()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return readRemoteResponseError(response)
	}
	if output == nil {
		_, err = io.Copy(io.Discard, response.Body)
		return err
	}
	return json.NewDecoder(response.Body).Decode(output)
}

type runnerJobRequest struct {
	RunnerID   string `json:"runnerId"`
	Repository string `json:"repository"`
	ID         string `json:"id"`
}

type runnerLogRequest struct {
	RunnerID   string `json:"runnerId"`
	Repository string `json:"repository"`
	ID         string `json:"id"`
	Offset     int64  `json:"offset"`
	Content    []byte `json:"content"`
}

type runnerCompleteRequest struct {
	RunnerID   string `json:"runnerId"`
	Repository string `json:"repository"`
	ID         string `json:"id"`
	Error      string `json:"error,omitempty"`
}

type remoteLogWriter struct {
	remote   *Remote
	ctx      context.Context
	runnerID string
	job      Job
	offset   int64
	mu       sync.Mutex
}

func (w *remoteLogWriter) Write(contents []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	const chunkSize = 48 * 1024
	for start := 0; start < len(contents); start += chunkSize {
		end := min(start+chunkSize, len(contents))
		offset, err := w.remote.appendLog(
			w.ctx,
			w.runnerID,
			w.job,
			w.offset,
			contents[start:end],
		)
		if err != nil {
			return 0, err
		}
		w.offset = offset
	}
	return len(contents), nil
}

type remoteResponseError struct {
	Status int
	Body   string
}

func (e *remoteResponseError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("runner API returned HTTP %d", e.Status)
	}
	return fmt.Sprintf("runner API returned HTTP %d: %s", e.Status, e.Body)
}

func readRemoteResponseError(response *http.Response) error {
	contents, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	return &remoteResponseError{
		Status: response.StatusCode,
		Body:   strings.TrimSpace(string(contents)),
	}
}

func waitForRemote(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-timer.C:
		return nil
	}
}
