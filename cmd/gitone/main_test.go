package main

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func setValidEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("LDAP_URL", "ldaps://directory.example:636")
	t.Setenv("LDAP_BASE_DN", "dc=example,dc=com")
	t.Setenv("GITONE_SESSION_HASH_KEY", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("h", 64))))
	t.Setenv("GITONE_SESSION_BLOCK_KEY", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("b", 32))))
	t.Setenv("GITONE_RUNNER_TOKEN", "")
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

	t.Run("invalid public URL", func(t *testing.T) {
		setValidEnvironment(t)
		if _, _, err := newServer([]string{"-public-url", "://invalid"}); err == nil {
			t.Fatal("invalid public URL was accepted")
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
