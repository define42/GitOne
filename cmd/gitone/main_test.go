package main

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type gracefulServerStub struct {
	listenStarted  chan struct{}
	listenFinished chan struct{}
	shutdownCalled chan struct{}
	listenErr      error
	shutdownErr    error
	finishOnce     sync.Once
	t              *testing.T
}

func newGracefulServerStub(t *testing.T) *gracefulServerStub {
	t.Helper()
	return &gracefulServerStub{
		listenStarted:  make(chan struct{}),
		listenFinished: make(chan struct{}),
		shutdownCalled: make(chan struct{}),
		t:              t,
	}
}

func (server *gracefulServerStub) ListenAndServe() error {
	close(server.listenStarted)
	<-server.listenFinished
	return server.listenErr
}

func (server *gracefulServerStub) Shutdown(ctx context.Context) error {
	if _, ok := ctx.Deadline(); !ok {
		server.t.Error("shutdown context has no deadline")
	}
	close(server.shutdownCalled)
	if server.shutdownErr == nil {
		server.finishOnce.Do(func() { close(server.listenFinished) })
	}
	return server.shutdownErr
}

func (server *gracefulServerStub) finish() {
	server.finishOnce.Do(func() { close(server.listenFinished) })
}

func setValidEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("LDAP_URL", "ldaps://directory.example:636")
	t.Setenv("LDAP_BASE_DN", "dc=example,dc=com")
	t.Setenv("LDAP_USER_DOMAIN", "example.com")
	t.Setenv("LDAP_CANONICAL_ATTRIBUTE", "mail")
	t.Setenv("GITONE_SESSION_HASH_KEY", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("h", 64))))
	t.Setenv("GITONE_SESSION_BLOCK_KEY", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("b", 32))))
	t.Setenv("GITONE_RUNNER_TOKEN", "")
	t.Setenv("GITONE_IMPORT_ALLOWLIST", "")
	clearTLSEnvironment(t)
}

func TestNewServer(t *testing.T) {
	setValidEnvironment(t)
	server, ephemeral, err := newServer([]string{
		"-root", t.TempDir(),
		"-listen", "127.0.0.1:9090",
		"-public-url", "https://git.example",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ephemeral ||
		server.Addr != "127.0.0.1:9090" ||
		server.Handler == nil ||
		server.ReadHeaderTimeout != 10*time.Second ||
		server.IdleTimeout != 2*time.Minute {
		t.Fatalf("unexpected server: %#v ephemeral=%v", server, ephemeral)
	}
}

func TestRunReturnsInvalidListenAddressWithEphemeralSessions(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("GITONE_SESSION_HASH_KEY", "")
	t.Setenv("GITONE_SESSION_BLOCK_KEY", "")

	err := run(context.Background(), []string{
		"-root", t.TempDir(),
		"-listen", "127.0.0.1:8080:extra",
	})
	if err == nil || !strings.Contains(err.Error(), "too many colons") {
		t.Fatalf("run error = %v, want invalid listen address", err)
	}
}

func TestRunReturnsConfigurationError(t *testing.T) {
	err := run(context.Background(), []string{"-unknown"})
	if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("run error = %v, want invalid flag error", err)
	}
}

func TestServeGracefullyShutsDown(t *testing.T) {
	server := newGracefulServerStub(t)
	server.listenErr = http.ErrServerClosed
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- serve(ctx, server, time.Second) }()

	<-server.listenStarted
	cancel()
	<-server.shutdownCalled
	if err := <-result; err != nil {
		t.Fatalf("serve returned %v", err)
	}
}

func TestServeReturnsListenError(t *testing.T) {
	want := errors.New("listen failed")
	server := newGracefulServerStub(t)
	server.listenErr = want
	server.finish()

	if err := serve(context.Background(), server, time.Second); !errors.Is(err, want) {
		t.Fatalf("serve returned %v, want %v", err, want)
	}
	select {
	case <-server.shutdownCalled:
		t.Fatal("shutdown called after listen failure")
	default:
	}
}

func TestServeReturnsShutdownError(t *testing.T) {
	want := errors.New("shutdown failed")
	server := newGracefulServerStub(t)
	server.shutdownErr = want
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- serve(ctx, server, time.Second) }()

	<-server.listenStarted
	cancel()
	if err := <-result; !errors.Is(err, want) {
		t.Fatalf("serve returned %v, want %v", err, want)
	}
	server.finish()
}

func TestServeReturnsListenErrorAfterShutdown(t *testing.T) {
	want := errors.New("listen failed during shutdown")
	server := newGracefulServerStub(t)
	server.listenErr = want
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- serve(ctx, server, time.Second) }()

	<-server.listenStarted
	cancel()
	if err := <-result; !errors.Is(err, want) {
		t.Fatalf("serve returned %v, want %v", err, want)
	}
}

func TestNewServerConfiguresACMEHTTPS(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("GITONE_TLS_MODE", "acme")
	t.Setenv("GITONE_TLS_DOMAINS", "git.example")
	server, ephemeral, err := newServer([]string{
		"-root", t.TempDir(),
		"-listen", "127.0.0.1:8443",
		"-public-url", "https://git.example",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ephemeral || server.transport.mode != transportACME ||
		server.transport.protocol() != "HTTPS" {
		t.Fatalf("unexpected HTTPS server: %#v ephemeral=%v", server, ephemeral)
	}
}

func TestNewServerRejectsInvalidConfiguration(t *testing.T) {
	t.Run("invalid flag", func(t *testing.T) {
		if _, _, err := newServer([]string{"-unknown"}); err == nil {
			t.Fatal("unknown flag was accepted")
		}
	})

	t.Run("missing LDAP URL", func(t *testing.T) {
		t.Setenv("LDAP_URL", "")
		t.Setenv("LDAP_BASE_DN", "")
		if _, _, err := newServer(nil); err == nil ||
			!strings.Contains(err.Error(), "LDAP_URL is required") {
			t.Fatalf("missing LDAP URL error = %v", err)
		}
	})

	t.Run("invalid LDAP configuration", func(t *testing.T) {
		setValidEnvironment(t)
		t.Setenv("LDAP_URL", "http://directory.example")
		if _, _, err := newServer(nil); err == nil {
			t.Fatal("invalid LDAP scheme was accepted")
		}
	})

	t.Run("missing LDAP user domain", func(t *testing.T) {
		setValidEnvironment(t)
		t.Setenv("LDAP_USER_DOMAIN", "")
		if _, _, err := newServer(nil); err == nil ||
			!strings.Contains(err.Error(), "LDAP user domain is required") {
			t.Fatalf("missing LDAP user domain error = %v", err)
		}
	})

	t.Run("invalid public URL", func(t *testing.T) {
		setValidEnvironment(t)
		if _, _, err := newServer([]string{"-public-url", "://invalid"}); err == nil {
			t.Fatal("invalid public URL was accepted")
		}
	})

	t.Run("ACME with HTTP public URL", func(t *testing.T) {
		setValidEnvironment(t)
		t.Setenv("GITONE_TLS_MODE", "acme")
		t.Setenv("GITONE_TLS_DOMAINS", "git.example")
		if _, _, err := newServer([]string{
			"-public-url", "http://git.example",
		}); err == nil || !strings.Contains(err.Error(), "absolute HTTPS") {
			t.Fatalf("ACME public URL error = %v", err)
		}
	})

	t.Run("ACME public URL not in certificate domains", func(t *testing.T) {
		setValidEnvironment(t)
		t.Setenv("GITONE_TLS_MODE", "acme")
		t.Setenv("GITONE_TLS_DOMAINS", "other.example")
		if _, _, err := newServer([]string{
			"-public-url", "https://git.example",
		}); err == nil || !strings.Contains(err.Error(), "must be included") {
			t.Fatalf("ACME public URL domain error = %v", err)
		}
	})

	t.Run("invalid import allowlist", func(t *testing.T) {
		setValidEnvironment(t)
		if _, _, err := newServer([]string{
			"-import-allowlist",
			"https://git.internal.example",
		}); err == nil || !strings.Contains(err.Error(), "allowlist") {
			t.Fatalf("invalid import allowlist error = %v", err)
		}
	})

	t.Run("incomplete session keys", func(t *testing.T) {
		setValidEnvironment(t)
		t.Setenv("GITONE_SESSION_BLOCK_KEY", "")
		if _, _, err := newServer(nil); err == nil {
			t.Fatal("incomplete session keys were accepted")
		}
	})

	t.Run("invalid session key length", func(t *testing.T) {
		setValidEnvironment(t)
		t.Setenv("GITONE_SESSION_HASH_KEY", base64.StdEncoding.EncodeToString([]byte("short")))
		if _, _, err := newServer(nil); err == nil {
			t.Fatal("short session hash key was accepted")
		}
	})

	t.Run("embedded runner flag", func(t *testing.T) {
		setValidEnvironment(t)
		if _, _, err := newServer([]string{"-runner"}); err == nil ||
			!strings.Contains(err.Error(), "flag provided but not defined") {
			t.Fatalf("embedded runner flag error = %v", err)
		}
	})
}
