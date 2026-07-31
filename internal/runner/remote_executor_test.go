package runner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/define42/GitOne/internal/repoconfig"
)

type lifecycleExecutorStub struct {
	startCalls          atomic.Int32
	shutdownCalls       atomic.Int32
	shutdownHadDeadline atomic.Bool
	shutdownWasLive     atomic.Bool
	startErr            error
	shutdownErr         error
}

func (*lifecycleExecutorStub) Run(context.Context, ExecuteRequest, io.Writer) error {
	return nil
}

func (e *lifecycleExecutorStub) Start(context.Context) error {
	e.startCalls.Add(1)
	return e.startErr
}

func (e *lifecycleExecutorStub) Shutdown(ctx context.Context) error {
	e.shutdownCalls.Add(1)
	_, hasDeadline := ctx.Deadline()
	e.shutdownHadDeadline.Store(hasDeadline)
	e.shutdownWasLive.Store(ctx.Err() == nil)
	return e.shutdownErr
}

func TestRemoteExecutorLifecycle(t *testing.T) {
	t.Run("canceled before start", func(t *testing.T) {
		var claims atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(
			response http.ResponseWriter,
			_ *http.Request,
		) {
			claims.Add(1)
			http.Error(response, "unexpected claim", http.StatusInternalServerError)
		}))
		defer server.Close()

		executor := &lifecycleExecutorStub{}
		remote := newRemoteWithExecutor(t, server.URL, executor)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := remote.Run(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled run error = %v", err)
		}
		if executor.startCalls.Load() != 0 ||
			executor.shutdownCalls.Load() != 0 ||
			claims.Load() != 0 {
			t.Fatalf(
				"calls: start=%d shutdown=%d claim=%d",
				executor.startCalls.Load(),
				executor.shutdownCalls.Load(),
				claims.Load(),
			)
		}
	})

	t.Run("start precedes claims and shutdown uses cleanup context", func(t *testing.T) {
		executor := &lifecycleExecutorStub{}
		server := httptest.NewServer(http.HandlerFunc(func(
			response http.ResponseWriter,
			_ *http.Request,
		) {
			if executor.startCalls.Load() != 1 {
				http.Error(response, "executor was not started", http.StatusInternalServerError)
				return
			}
			http.Error(response, "unauthorized", http.StatusUnauthorized)
		}))
		defer server.Close()

		remote := newRemoteWithExecutor(t, server.URL, executor)
		err := remote.Run(context.Background())
		var responseErr *remoteResponseError
		if !errors.As(err, &responseErr) || responseErr.Status != http.StatusUnauthorized {
			t.Fatalf("run error = %v", err)
		}
		if executor.startCalls.Load() != 1 || executor.shutdownCalls.Load() != 1 {
			t.Fatalf(
				"calls: start=%d shutdown=%d",
				executor.startCalls.Load(),
				executor.shutdownCalls.Load(),
			)
		}
		if !executor.shutdownHadDeadline.Load() || !executor.shutdownWasLive.Load() {
			t.Fatalf(
				"shutdown context: deadline=%v live=%v",
				executor.shutdownHadDeadline.Load(),
				executor.shutdownWasLive.Load(),
			)
		}
	})

	t.Run("failed start is cleaned up", func(t *testing.T) {
		startErr := errors.New("prepare failed")
		shutdownErr := errors.New("cleanup failed")
		executor := &lifecycleExecutorStub{
			startErr: startErr, shutdownErr: shutdownErr,
		}
		remote := newRemoteWithExecutor(t, "https://gitone.example", executor)
		err := remote.Run(context.Background())
		if !errors.Is(err, startErr) || !errors.Is(err, shutdownErr) {
			t.Fatalf("start and shutdown error = %v", err)
		}
		if executor.startCalls.Load() != 1 || executor.shutdownCalls.Load() != 1 {
			t.Fatalf(
				"calls: start=%d shutdown=%d",
				executor.startCalls.Load(),
				executor.shutdownCalls.Load(),
			)
		}
	})
}

type reservationStub struct {
	assigned    atomic.Int32
	runs        atomic.Int32
	releases    atomic.Int32
	runHook     func(context.Context) error
	releaseErr  error
	releaseHook func(context.Context)
}

func (r *reservationStub) Assign() {
	r.assigned.Add(1)
}

func (r *reservationStub) Run(
	ctx context.Context,
	_ ExecuteRequest,
	_ io.Writer,
) error {
	r.runs.Add(1)
	if r.runHook != nil {
		return r.runHook(ctx)
	}
	return nil
}

func (r *reservationStub) Release(ctx context.Context) error {
	r.releases.Add(1)
	if r.releaseHook != nil {
		r.releaseHook(ctx)
	}
	return r.releaseErr
}

type reservingExecutorStub struct {
	reserves atomic.Int32
	reserve  func(context.Context, int32) (ExecutorReservation, error)
}

func (*reservingExecutorStub) Run(context.Context, ExecuteRequest, io.Writer) error {
	return errors.New("unreserved executor was used")
}

func (e *reservingExecutorStub) Reserve(ctx context.Context) (ExecutorReservation, error) {
	call := e.reserves.Add(1)
	return e.reserve(ctx, call)
}

func TestRemoteReservesBeforeClaimAndReleasesUnusedCapacity(t *testing.T) {
	for _, test := range []struct {
		name        string
		claimStatus int
	}{
		{name: "empty claim", claimStatus: http.StatusOK},
		{name: "failed claim", claimStatus: http.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reservation := &reservationStub{}
			reservation.releaseHook = func(cleanupContext context.Context) {
				if _, ok := cleanupContext.Deadline(); !ok || cleanupContext.Err() != nil {
					t.Errorf("release context has no live deadline: %v", cleanupContext.Err())
				}
				cancel()
			}
			executor := &reservingExecutorStub{
				reserve: func(context.Context, int32) (ExecutorReservation, error) {
					return reservation, nil
				},
			}
			server := httptest.NewServer(http.HandlerFunc(func(
				response http.ResponseWriter,
				_ *http.Request,
			) {
				if executor.reserves.Load() == 0 {
					http.Error(response, "claim preceded reservation", http.StatusInternalServerError)
					return
				}
				if test.claimStatus != http.StatusOK {
					http.Error(response, "unauthorized", test.claimStatus)
					return
				}
				_ = json.NewEncoder(response).Encode(map[string]any{"lease": nil})
			}))
			defer server.Close()

			remote := newRemoteWithExecutor(t, server.URL, executor)
			err := remote.Run(ctx)
			if test.claimStatus == http.StatusOK {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("empty-claim run error = %v", err)
				}
			} else {
				var responseErr *remoteResponseError
				if !errors.As(err, &responseErr) || responseErr.Status != test.claimStatus {
					t.Fatalf("failed-claim run error = %v", err)
				}
			}
			if reservation.assigned.Load() != 0 || reservation.runs.Load() != 0 ||
				reservation.releases.Load() != 1 {
				t.Fatalf(
					"reservation: assigned=%d runs=%d releases=%d",
					reservation.assigned.Load(),
					reservation.runs.Load(),
					reservation.releases.Load(),
				)
			}
		})
	}
}

func TestRemoteAssignsAndReleasesReservationBeforeCompletion(t *testing.T) {
	teardownErr := errors.New("teardown failed")
	reservation := &reservationStub{releaseErr: teardownErr}
	executor := &reservingExecutorStub{}
	secondReserve := make(chan struct{})
	executor.reserve = func(ctx context.Context, call int32) (ExecutorReservation, error) {
		if call == 1 {
			return reservation, nil
		}
		close(secondReserve)
		<-ctx.Done()
		return nil, ctx.Err()
	}

	archive := sourceArchive(t)
	completed := make(chan runnerCompleteRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		switch request.URL.Path {
		case "/api/runner/jobs/claim":
			lease := Lease{
				Job: Job{
					ID: "reserved-build", Repository: "engineering/api", Branch: "main",
					Commit: strings.Repeat("1", 40), Image: "alpine:3",
				},
				Config:       buildConfigForRemoteExecutorTest(),
				LeaseSeconds: 30,
			}
			_ = json.NewEncoder(response).Encode(map[string]any{"lease": lease})
		case "/api/runner/source":
			if reservation.assigned.Load() != 1 {
				http.Error(response, "reservation was not assigned", http.StatusConflict)
				return
			}
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
		case "/api/runner/jobs/complete":
			var input runnerCompleteRequest
			if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
				http.Error(response, err.Error(), http.StatusBadRequest)
				return
			}
			if reservation.releases.Load() != 1 {
				http.Error(response, "reservation was not released", http.StatusConflict)
				return
			}
			completed <- input
			response.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	remote := newRemoteWithExecutor(t, server.URL, executor)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- remote.Run(ctx)
	}()

	select {
	case completion := <-completed:
		if !strings.Contains(completion.Error, teardownErr.Error()) {
			t.Fatalf("completion error = %q", completion.Error)
		}
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("build was not completed")
	}
	select {
	case <-secondReserve:
		cancel()
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("runner did not finish reporting completion")
	}
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("run error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("remote runner did not stop")
	}
	if reservation.assigned.Load() != 1 || reservation.runs.Load() != 1 ||
		reservation.releases.Load() != 1 {
		t.Fatalf(
			"reservation: assigned=%d runs=%d releases=%d",
			reservation.assigned.Load(),
			reservation.runs.Load(),
			reservation.releases.Load(),
		)
	}
}

func TestRemoteHeartbeatsWhileTimedOutVMIsReleased(t *testing.T) {
	releaseStarted := make(chan struct{})
	heartbeatDuringRelease := make(chan struct{})
	var heartbeatOnce sync.Once
	reservation := &reservationStub{
		runHook: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
		releaseHook: func(context.Context) {
			close(releaseStarted)
			select {
			case <-heartbeatDuringRelease:
			case <-time.After(3 * time.Second):
				t.Error("lease heartbeat stopped while timed-out VM was being released")
			}
		},
	}
	executor := &reservingExecutorStub{}
	executor.reserve = func(ctx context.Context, call int32) (ExecutorReservation, error) {
		if call == 1 {
			return reservation, nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}

	archive := sourceArchive(t)
	var claimed atomic.Bool
	completed := make(chan runnerCompleteRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		switch request.URL.Path {
		case "/api/runner/jobs/claim":
			if claimed.Swap(true) {
				_ = json.NewEncoder(response).Encode(map[string]any{"lease": nil})
				return
			}
			lease := Lease{
				Job: Job{
					ID: "heartbeat-release", Repository: "engineering/api", Branch: "main",
					Commit: strings.Repeat("2", 40), Image: "alpine:3",
				},
				Config: repoconfig.BuildConfig{
					Image: "alpine:3", Script: []string{"sleep 30"}, TimeoutSeconds: 1,
				},
				LeaseSeconds: 3,
			}
			_ = json.NewEncoder(response).Encode(map[string]any{"lease": lease})
		case "/api/runner/source":
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
			select {
			case <-releaseStarted:
				heartbeatOnce.Do(func() { close(heartbeatDuringRelease) })
			default:
			}
			_ = json.NewEncoder(response).Encode(map[string]any{
				"leaseExpiresAt": time.Now().UTC().Add(3 * time.Second),
			})
		case "/api/runner/jobs/complete":
			var input runnerCompleteRequest
			if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
				http.Error(response, err.Error(), http.StatusBadRequest)
				return
			}
			completed <- input
			response.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	remote := newRemoteWithExecutor(t, server.URL, executor)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- remote.Run(ctx) }()

	select {
	case completion := <-completed:
		if !strings.Contains(completion.Error, "timed out") {
			t.Fatalf("completion error = %q", completion.Error)
		}
	case <-time.After(6 * time.Second):
		cancel()
		t.Fatal("timed-out build was not completed")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("run error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("remote runner did not stop")
	}
}

func buildConfigForRemoteExecutorTest() repoconfig.BuildConfig {
	return repoconfig.BuildConfig{
		Image: "alpine:3", Script: []string{"true"}, TimeoutSeconds: 30,
	}
}

func newRemoteWithExecutor(t *testing.T, serverURL string, executor Executor) *Remote {
	t.Helper()
	remote, err := NewRemote(RemoteConfig{
		URL: serverURL, Token: "token", ID: "runner-one",
		WorkRoot: t.TempDir(), Workers: 1, PollInterval: 250 * time.Millisecond,
		Executor: executor,
	})
	if err != nil {
		t.Fatal(err)
	}
	return remote
}

func TestBoundedExecutorCleanupTimeout(t *testing.T) {
	tests := []struct {
		name     string
		input    time.Duration
		expected time.Duration
	}{
		{name: "default", input: 0, expected: defaultExecutorCleanupTimeout},
		{name: "configured", input: 2 * time.Minute, expected: 2 * time.Minute},
		{name: "bounded", input: time.Hour, expected: maximumExecutorCleanupTimeout},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if actual := boundedExecutorCleanupTimeout(test.input); actual != test.expected {
				t.Fatalf("timeout = %s, want %s", actual, test.expected)
			}
		})
	}
}
