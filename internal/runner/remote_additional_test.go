package runner

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
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/define42/GitOne/internal/repoconfig"
)

type executorFunc func(context.Context, ExecuteRequest, io.Writer) error

func (f executorFunc) Run(
	ctx context.Context,
	request ExecuteRequest,
	output io.Writer,
) error {
	return f(ctx, request, output)
}

func TestNewRemoteValidatesConfigurationAndUsesDefaults(t *testing.T) {
	valid := RemoteConfig{
		URL: "https://gitone.example", Token: "token", ID: "runner-one",
		WorkRoot: t.TempDir(), Workers: 1, PollInterval: time.Second,
		Executor: executorFunc(func(context.Context, ExecuteRequest, io.Writer) error {
			return nil
		}),
	}
	tests := []struct {
		name   string
		mutate func(*RemoteConfig)
	}{
		{name: "invalid URL", mutate: func(c *RemoteConfig) { c.URL = "relative" }},
		{name: "missing token", mutate: func(c *RemoteConfig) { c.Token = "" }},
		{name: "invalid ID", mutate: func(c *RemoteConfig) { c.ID = "bad runner" }},
		{name: "missing work root", mutate: func(c *RemoteConfig) { c.WorkRoot = "" }},
		{name: "negative workers", mutate: func(c *RemoteConfig) { c.Workers = -1 }},
		{name: "too many workers", mutate: func(c *RemoteConfig) { c.Workers = 33 }},
		{
			name: "ID too long for worker suffix",
			mutate: func(c *RemoteConfig) {
				c.ID = strings.Repeat("r", maximumRunnerIDLength-2)
				c.Workers = 10
			},
		},
		{
			name:   "short poll interval",
			mutate: func(c *RemoteConfig) { c.PollInterval = 100 * time.Millisecond },
		},
		{
			name:   "long poll interval",
			mutate: func(c *RemoteConfig) { c.PollInterval = 2 * time.Minute },
		},
		{name: "missing executor", mutate: func(c *RemoteConfig) { c.Executor = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := valid
			test.mutate(&config)
			if _, err := NewRemote(config); err == nil {
				t.Fatal("invalid remote configuration was accepted")
			}
		})
	}

	defaults := valid
	defaults.WorkRoot = filepath.Join(t.TempDir(), "new-work-root")
	defaults.Workers = 0
	defaults.PollInterval = 0
	defaults.HTTPClient = nil
	remote, err := NewRemote(defaults)
	if err != nil {
		t.Fatal(err)
	}
	if remote.workers != 1 || remote.pollInterval != 2*time.Second ||
		remote.client == nil {
		t.Fatalf("remote defaults = %#v", remote)
	}
	if _, err = os.Stat(defaults.WorkRoot); err != nil {
		t.Fatal(err)
	}

	parentFile := filepath.Join(t.TempDir(), "parent")
	if err = os.WriteFile(parentFile, []byte("not a directory"), 0o640); err != nil {
		t.Fatal(err)
	}
	badWorkRoot := valid
	badWorkRoot.WorkRoot = filepath.Join(parentFile, "work")
	if _, err = NewRemote(badWorkRoot); err == nil {
		t.Fatal("work-root creation failure was ignored")
	}
}

func TestNewRemoteDerivesLibvirtWorkersFromMaximumInstances(t *testing.T) {
	libvirtConfig := validLibvirtTestConfig()
	libvirtConfig.IdleCount = 2
	libvirtConfig.MaxInstances = maximumLibvirtInstances
	executor, err := newLibvirtExecutorWithProvider(libvirtConfig, &fakeVMProvider{})
	if err != nil {
		t.Fatal(err)
	}
	remote, err := NewRemote(RemoteConfig{
		URL: "https://gitone.example", Token: "token", ID: "runner-one",
		WorkRoot: t.TempDir(), Workers: 1, PollInterval: time.Second,
		Executor: executor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if remote.workers != libvirtConfig.MaxInstances {
		t.Fatalf("libvirt workers = %d, want %d", remote.workers, libvirtConfig.MaxInstances)
	}
}

func TestRemotePostResponsesAndTransportErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		if request.Header.Get("Authorization") != "Bearer token" ||
			request.Header.Get("Content-Type") != "application/json" {
			http.Error(response, "bad headers", http.StatusBadRequest)
			return
		}
		switch request.URL.Path {
		case "/success":
			response.WriteHeader(http.StatusNoContent)
		case "/json":
			_ = json.NewEncoder(response).Encode(map[string]int{"value": 42})
		case "/invalid-json":
			_, _ = io.WriteString(response, "{")
		case "/body-error":
			http.Error(response, "brew failed", http.StatusTeapot)
		case "/empty-error":
			response.WriteHeader(http.StatusInternalServerError)
		default:
			http.NotFound(response, request)
		}
	}))
	remote := newHTTPRemote(t, server.URL)

	if err := remote.post(context.Background(), "/success", map[string]bool{"ok": true}, nil); err != nil {
		t.Fatal(err)
	}
	var output struct {
		Value int `json:"value"`
	}
	if err := remote.post(context.Background(), "/json", struct{}{}, &output); err != nil {
		t.Fatal(err)
	}
	if output.Value != 42 {
		t.Fatalf("JSON response = %#v", output)
	}
	if err := remote.post(
		context.Background(),
		"/invalid-json",
		struct{}{},
		&output,
	); err == nil {
		t.Fatal("invalid JSON response was accepted")
	}

	err := remote.post(context.Background(), "/body-error", struct{}{}, nil)
	var responseErr *remoteResponseError
	if !errors.As(err, &responseErr) ||
		responseErr.Status != http.StatusTeapot ||
		responseErr.Error() != "runner API returned HTTP 418: brew failed" {
		t.Fatalf("body response error = %#v, %v", responseErr, err)
	}
	err = remote.post(context.Background(), "/empty-error", struct{}{}, nil)
	if !errors.As(err, &responseErr) ||
		responseErr.Error() != "runner API returned HTTP 500" {
		t.Fatalf("empty response error = %#v, %v", responseErr, err)
	}
	if err = remote.post(context.Background(), "/success", make(chan int), nil); err == nil {
		t.Fatal("JSON marshal failure was ignored")
	}

	server.Close()
	if err = remote.post(context.Background(), "/success", struct{}{}, nil); err == nil {
		t.Fatal("transport failure was ignored")
	}
}

func TestRemoteDownloadAndCompleteErrors(t *testing.T) {
	t.Run("source status", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(
			response http.ResponseWriter,
			_ *http.Request,
		) {
			http.Error(response, "lease expired", http.StatusConflict)
		}))
		defer server.Close()
		remote := newHTTPRemote(t, server.URL)
		err := remote.downloadSource(
			context.Background(),
			"runner-one",
			Job{ID: "build", Repository: "engineering/api"},
			t.TempDir(),
		)
		var responseErr *remoteResponseError
		if !errors.As(err, &responseErr) ||
			responseErr.Status != http.StatusConflict {
			t.Fatalf("source status error = %v", err)
		}
	})

	t.Run("invalid source archive", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(
			response http.ResponseWriter,
			_ *http.Request,
		) {
			_, _ = io.WriteString(response, "not gzip")
		}))
		defer server.Close()
		remote := newHTTPRemote(t, server.URL)
		if err := remote.downloadSource(
			context.Background(),
			"runner-one",
			Job{ID: "build", Repository: "engineering/api"},
			t.TempDir(),
		); err == nil {
			t.Fatal("invalid source archive was accepted")
		}
	})

	t.Run("completion preserves build error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(
			response http.ResponseWriter,
			request *http.Request,
		) {
			if request.URL.Path != "/api/runner/jobs/complete" {
				http.NotFound(response, request)
				return
			}
			response.WriteHeader(http.StatusNoContent)
		}))
		defer server.Close()
		remote := newHTTPRemote(t, server.URL)
		buildErr := errors.New("build failed")
		err := remote.complete(
			context.Background(),
			"runner-one",
			Job{ID: "build", Repository: "engineering/api"},
			buildErr,
		)
		if !errors.Is(err, buildErr) {
			t.Fatalf("completion error = %v", err)
		}
	})

	t.Run("completion API error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(
			response http.ResponseWriter,
			_ *http.Request,
		) {
			http.Error(response, "unavailable", http.StatusServiceUnavailable)
		}))
		defer server.Close()
		remote := newHTTPRemote(t, server.URL)
		err := remote.complete(
			context.Background(),
			"runner-one",
			Job{ID: "build", Repository: "engineering/api"},
			nil,
		)
		if err == nil || !strings.Contains(err.Error(), "complete build") {
			t.Fatalf("completion API error = %v", err)
		}
	})

	t.Run("completion bounds long build errors", func(t *testing.T) {
		received := make(chan runnerCompleteRequest, 1)
		server := httptest.NewServer(http.HandlerFunc(func(
			response http.ResponseWriter,
			request *http.Request,
		) {
			var input runnerCompleteRequest
			if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
				http.Error(response, err.Error(), http.StatusBadRequest)
				return
			}
			received <- input
			response.WriteHeader(http.StatusNoContent)
		}))
		defer server.Close()
		remote := newHTTPRemote(t, server.URL)
		buildErr := errors.New(strings.Repeat("ø", maximumCompletionErrorBytes))
		if err := remote.postCompletion(
			context.Background(),
			"runner-one",
			Job{ID: "build", Repository: "engineering/api"},
			buildErr,
		); err != nil {
			t.Fatal(err)
		}
		completion := <-received
		if len(completion.Error) > maximumCompletionErrorBytes ||
			!strings.HasSuffix(completion.Error, "... [truncated]") ||
			!utf8.ValidString(completion.Error) {
			t.Fatalf("bounded completion error has %d bytes: %q", len(completion.Error), completion.Error)
		}
	})
}

func TestRemoteLogWriterChunksAndReportsErrors(t *testing.T) {
	var mu sync.Mutex
	var chunks [][]byte
	var offset int64
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		var input runnerLogRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			http.Error(response, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if input.Offset != offset {
			http.Error(response, "offset", http.StatusConflict)
			return
		}
		chunks = append(chunks, append([]byte(nil), input.Content...))
		offset += int64(len(input.Content))
		_ = json.NewEncoder(response).Encode(map[string]int64{"offset": offset})
	}))
	remote := newHTTPRemote(t, server.URL)
	writer := &remoteLogWriter{
		remote: remote, ctx: context.Background(), runnerID: "runner-one",
		job: Job{ID: "build", Repository: "engineering/api"},
	}
	contents := bytes.Repeat([]byte("x"), 100*1024)
	written, err := writer.Write(contents)
	if err != nil || written != len(contents) {
		t.Fatalf("chunked write = %d, %v", written, err)
	}
	mu.Lock()
	if len(chunks) != 3 || offset != int64(len(contents)) {
		t.Fatalf("chunks=%d offset=%d", len(chunks), offset)
	}
	mu.Unlock()
	server.Close()
	if written, err = writer.Write([]byte("failure")); err == nil || written != 0 {
		t.Fatalf("failed log write = %d, %v", written, err)
	}
}

func TestRemoteRunHandlesAuthenticationRetryAndCancellation(t *testing.T) {
	t.Run("authentication failure", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(
			response http.ResponseWriter,
			_ *http.Request,
		) {
			http.Error(response, "unauthorized", http.StatusUnauthorized)
		}))
		defer server.Close()
		remote := newHTTPRemote(t, server.URL)
		remote.workers = 2
		err := remote.Run(context.Background())
		var responseErr *remoteResponseError
		if !errors.As(err, &responseErr) ||
			responseErr.Status != http.StatusUnauthorized {
			t.Fatalf("authentication run error = %v", err)
		}
	})

	t.Run("transient failure is retried", func(t *testing.T) {
		var claims atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(
			response http.ResponseWriter,
			_ *http.Request,
		) {
			if claims.Add(1) == 1 {
				http.Error(response, "temporary", http.StatusInternalServerError)
				return
			}
			http.Error(response, "forbidden", http.StatusForbidden)
		}))
		defer server.Close()
		remote := newHTTPRemote(t, server.URL)
		err := remote.Run(context.Background())
		var responseErr *remoteResponseError
		if claims.Load() < 2 || !errors.As(err, &responseErr) ||
			responseErr.Status != http.StatusForbidden {
			t.Fatalf("retry claims=%d error=%v", claims.Load(), err)
		}
	})

	t.Run("idle cancellation", func(t *testing.T) {
		claimed := make(chan struct{}, 1)
		server := httptest.NewServer(http.HandlerFunc(func(
			response http.ResponseWriter,
			_ *http.Request,
		) {
			claimed <- struct{}{}
			_ = json.NewEncoder(response).Encode(map[string]any{"lease": nil})
		}))
		defer server.Close()
		remote := newHTTPRemote(t, server.URL)
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			result <- remote.Run(ctx)
		}()
		select {
		case <-claimed:
			cancel()
		case <-time.After(2 * time.Second):
			t.Fatal("runner did not claim")
		}
		select {
		case err := <-result:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled run error = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("runner did not stop")
		}
	})
}

func TestRemoteHeartbeatReportsAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		_ *http.Request,
	) {
		http.Error(response, "heartbeat failed", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	remote := newHTTPRemote(t, server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errorsChannel := make(chan error, 1)
	done := make(chan struct{})
	var requests sync.Mutex
	go remote.heartbeat(
		ctx,
		cancel,
		"runner-one",
		Lease{
			Job: Job{
				ID: "build", Repository: "engineering/api",
			},
			LeaseSeconds: 1,
		},
		errorsChannel,
		done,
		&requests,
	)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("heartbeat did not finish")
	}
	select {
	case err := <-errorsChannel:
		if !strings.Contains(err.Error(), "heartbeat build") {
			t.Fatalf("heartbeat error = %v", err)
		}
	default:
		t.Fatal("heartbeat error was not reported")
	}
}

func TestRemoteStopsCanceledBuildWithoutCompletingIt(t *testing.T) {
	archive := sourceArchive(t)
	var heartbeatCalls atomic.Int32
	var completionCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/runner/source":
			response.Header().Set("Content-Type", "application/gzip")
			_, _ = response.Write(archive)
		case "/api/runner/jobs/log":
			var input runnerLogRequest
			if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
				http.Error(response, err.Error(), http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(response).Encode(map[string]int64{
				"offset": input.Offset + int64(len(input.Content)),
			})
		case "/api/runner/jobs/heartbeat":
			heartbeatCalls.Add(1)
			_ = json.NewEncoder(response).Encode(runnerHeartbeatResponse{
				LeaseExpiresAt: time.Now().UTC(),
				Canceled:       true,
			})
		case "/api/runner/jobs/complete":
			completionCalls.Add(1)
			response.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	executorStopped := make(chan struct{})
	remote, err := NewRemote(RemoteConfig{
		URL: server.URL, Token: "token", ID: "runner-one", WorkRoot: t.TempDir(),
		Workers: 1, PollInterval: 250 * time.Millisecond, HTTPClient: server.Client(),
		Executor: executorFunc(func(
			ctx context.Context,
			_ ExecuteRequest,
			_ io.Writer,
		) error {
			<-ctx.Done()
			close(executorStopped)
			return ctx.Err()
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	lease := Lease{
		Job: Job{
			ID: "canceled-build", Repository: "engineering/api", Branch: "main",
			Commit: strings.Repeat("1", 40), Image: "alpine:3", RunnerID: "runner-one",
		},
		Config: repoconfig.JobConfig{
			Image: "alpine:3", Script: []string{"sleep 30"}, TimeoutSeconds: 30,
		},
		LeaseSeconds: 1,
	}
	result := make(chan error, 1)
	go func() {
		result <- remote.runLease(context.Background(), "runner-one", lease, remote.executor)
	}()
	select {
	case err = <-result:
		if err != nil {
			t.Fatalf("canceled build result = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("remote build did not stop after cancellation")
	}
	select {
	case <-executorStopped:
	default:
		t.Fatal("executor did not observe cancellation")
	}
	if heartbeatCalls.Load() == 0 || completionCalls.Load() != 0 {
		t.Fatalf(
			"heartbeat calls=%d, completion calls=%d",
			heartbeatCalls.Load(),
			completionCalls.Load(),
		)
	}
}

func TestRemoteHeartbeatRequestIsBoundedBelowLease(t *testing.T) {
	requestStarted := make(chan struct{}, 1)
	releaseHandler := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(
		_ http.ResponseWriter,
		_ *http.Request,
	) {
		requestStarted <- struct{}{}
		<-releaseHandler
	}))
	defer func() {
		close(releaseHandler)
		server.Close()
	}()
	remote := newHTTPRemote(t, server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errorsChannel := make(chan error, 1)
	done := make(chan struct{})
	var requests sync.Mutex
	startedAt := time.Now()
	go remote.heartbeat(
		ctx,
		cancel,
		"runner-one",
		Lease{
			Job:          Job{ID: "build", Repository: "engineering/api"},
			LeaseSeconds: 3,
		},
		errorsChannel,
		done,
		&requests,
	)
	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("heartbeat request did not start")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("hung heartbeat request was not canceled")
	}
	if elapsed := time.Since(startedAt); elapsed >= 3*time.Second {
		t.Fatalf("heartbeat failed after the lease could expire: %s", elapsed)
	}
	select {
	case err := <-errorsChannel:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("heartbeat timeout error = %v", err)
		}
	default:
		t.Fatal("heartbeat timeout was not reported")
	}
}

func TestRemoteRetriesCompletionWhileHeartbeatContinues(t *testing.T) {
	var completionCalls atomic.Int32
	var heartbeatCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		switch request.URL.Path {
		case "/api/runner/jobs/complete":
			if completionCalls.Add(1) == 1 {
				time.Sleep(750 * time.Millisecond)
				return
			}
			response.WriteHeader(http.StatusNoContent)
		case "/api/runner/jobs/heartbeat":
			heartbeatCalls.Add(1)
			response.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	remote := newHTTPRemote(t, server.URL)
	ctx, stopHeartbeat := context.WithCancel(context.Background())
	defer stopHeartbeat()
	lease := Lease{
		Job:          Job{ID: "build", Repository: "engineering/api"},
		LeaseSeconds: 3,
	}
	heartbeatErrors := make(chan error, 1)
	heartbeatDone := make(chan struct{})
	var requests sync.Mutex
	go remote.heartbeat(
		ctx,
		stopHeartbeat,
		"runner-one",
		lease,
		heartbeatErrors,
		heartbeatDone,
		&requests,
	)
	if err := remote.completeWithHeartbeat(
		ctx,
		"runner-one",
		lease,
		nil,
		stopHeartbeat,
		&requests,
	); err != nil {
		t.Fatal(err)
	}
	<-heartbeatDone
	if completionCalls.Load() != 2 || heartbeatCalls.Load() == 0 {
		t.Fatalf(
			"completion calls=%d heartbeat calls=%d",
			completionCalls.Load(),
			heartbeatCalls.Load(),
		)
	}
	select {
	case err := <-heartbeatErrors:
		t.Fatalf("heartbeat failed during completion retry: %v", err)
	default:
	}
}

func TestWaitForRemoteTimerAndCancellation(t *testing.T) {
	if err := waitForRemote(context.Background(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitForRemote(ctx, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled wait error = %v", err)
	}
}

func newHTTPRemote(t *testing.T, serverURL string) *Remote {
	t.Helper()
	remote, err := NewRemote(RemoteConfig{
		URL: serverURL, Token: "token", ID: "runner-one",
		WorkRoot: t.TempDir(), Workers: 1, PollInterval: 250 * time.Millisecond,
		Executor: executorFunc(func(context.Context, ExecuteRequest, io.Writer) error {
			return nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	return remote
}
