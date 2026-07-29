package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/define42/GitOne/internal/runner"
)

func main() {
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	err := run(ctx, os.Args[1:])
	stop()
	if err != nil &&
		!errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}

func run(ctx context.Context, args []string) error {
	remote, runnerID, serverURL, err := newRemoteRunner(args)
	if err != nil {
		return err
	}
	log.Printf("GitOne runner %s polling %s", runnerID, serverURL)
	return remote.Run(ctx)
}

func newRemoteRunner(args []string) (*runner.Remote, string, string, error) {
	flags := flag.NewFlagSet("gitone-runner", flag.ContinueOnError)
	serverURL := flags.String("runner-url", "http://gitone:8080", "GitOne server URL")
	token := flags.String(
		"runner-token",
		os.Getenv("GITONE_RUNNER_TOKEN"),
		"shared remote runner token",
	)
	runnerID := flags.String("runner-id", "gitone-runner", "unique runner ID")
	command := flags.String("runner-command", "docker", "Docker-compatible runner command")
	workers := flags.Int("runner-workers", 1, "number of concurrent builds")
	workRoot := flags.String(
		"runner-work-root",
		"/var/lib/gitone-runner",
		"host-visible runner workspace root",
	)
	pollInterval := flags.Duration("runner-poll-interval", 2*time.Second, "idle polling interval")
	if err := flags.Parse(args); err != nil {
		return nil, "", "", err
	}
	if flags.NArg() != 0 {
		return nil, "", "", fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	remote, err := runner.NewRemote(runner.RemoteConfig{
		URL:          *serverURL,
		Token:        *token,
		ID:           *runnerID,
		WorkRoot:     *workRoot,
		Workers:      *workers,
		PollInterval: *pollInterval,
		Executor:     runner.ContainerExecutor{Command: *command},
	})
	if err != nil {
		return nil, "", "", err
	}
	return remote, *runnerID, *serverURL, nil
}
