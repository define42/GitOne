package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
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
	if err := run(context.Background(), []string{"-help"}); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help error = %v, want flag.ErrHelp", err)
	}
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
		{
			name: "unknown executor", token: "test-token",
			args: []string{"-runner-executor", "shell"}, message: "executor",
		},
		{
			name: "invalid libvirt memory", token: "test-token",
			args: []string{"-libvirt-memory-mib", "128"}, message: "memory",
		},
		{
			name: "invalid libvirt network", token: "test-token",
			args: []string{"-libvirt-network-cidr", "10.20.0.0/24"}, message: "CIDR",
		},
		{
			name: "insecure Flatcar URL", token: "test-token",
			args:    []string{"-libvirt-base-image-url", "http://example.test/flatcar.img"},
			message: "HTTPS",
		},
		{
			name: "invalid Flatcar digest", token: "test-token",
			args:    []string{"-libvirt-base-image-sha512", "bad"},
			message: "SHA-512",
		},
		{
			name: "removed SSH key flag", token: "test-token",
			args:    []string{"-libvirt-ssh-key", "/tmp/id_ed25519"},
			message: "flag provided but not defined",
		},
		{
			name: "removed SSH command flag", token: "test-token",
			args:    []string{"-libvirt-ssh-command", "ssh"},
			message: "flag provided but not defined",
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

func TestCommaSeparatedValues(t *testing.T) {
	values := commaSeparatedValues(" https://mirror-one.example,registry:5000 ")
	if len(values) != 2 ||
		values[0] != "https://mirror-one.example" ||
		values[1] != "registry:5000" {
		t.Fatalf("values = %#v", values)
	}
	if values = commaSeparatedValues("  "); values != nil {
		t.Fatalf("blank values = %#v", values)
	}
}

func TestIsOnlyContextTermination(t *testing.T) {
	if !isOnlyContextTermination(fmt.Errorf("runner stopped: %w", context.Canceled)) {
		t.Fatal("wrapped cancellation was not recognized")
	}
	if isOnlyContextTermination(errors.Join(
		context.Canceled,
		fmt.Errorf("shutdown remote build executor: %w", context.DeadlineExceeded),
	)) {
		t.Fatal("shutdown deadline was hidden by cancellation")
	}
	if isOnlyContextTermination(errors.Join(
		context.Canceled,
		errors.New("VM cleanup failed"),
	)) {
		t.Fatal("cleanup failure was hidden by cancellation")
	}
}
