package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestNewRemoteRunner(t *testing.T) {
	t.Setenv("GITONE_RUNNER_TOKEN", "test-token")
	remote, runnerID, serverURL, err := newRemoteRunner([]string{
		"-runner-url", "https://gitone.example",
		"-runner-id", "build-server-1",
		"-runner-work-root", t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if remote == nil ||
		runnerID != "build-server-1" ||
		serverURL != "https://gitone.example" {
		t.Fatalf(
			"unexpected runner: remote=%#v id=%q url=%q",
			remote,
			runnerID,
			serverURL,
		)
	}
}

func TestRunReturnsConfigurationAndCancellationErrors(t *testing.T) {
	t.Setenv("GITONE_RUNNER_TOKEN", "")
	if err := run(context.Background(), nil); err == nil {
		t.Fatal("run accepted a missing token")
	}

	t.Setenv("GITONE_RUNNER_TOKEN", "test-token")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := run(ctx, []string{
		"-runner-url", "http://127.0.0.1:1",
		"-runner-work-root", t.TempDir(),
		"-runner-poll-interval", "250ms",
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled run returned %v", err)
	}
}

func TestNewRemoteRunnerRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		token   string
		args    []string
		message string
	}{
		{name: "missing token", message: "token"},
		{
			name: "invalid workers", token: "test-token",
			args: []string{"-runner-workers", "-1"}, message: "workers",
		},
		{
			name: "unexpected argument", token: "test-token",
			args: []string{"worker"}, message: "unexpected arguments",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("GITONE_RUNNER_TOKEN", test.token)
			if _, _, _, err := newRemoteRunner(test.args); err == nil ||
				!strings.Contains(err.Error(), test.message) {
				t.Fatalf("error = %v, want message containing %q", err, test.message)
			}
		})
	}
}
