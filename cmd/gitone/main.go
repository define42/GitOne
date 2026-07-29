package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/define42/GitOne/internal/auth"
	"github.com/define42/GitOne/internal/runner"
	app "github.com/define42/GitOne/internal/server"
	"github.com/define42/GitOne/internal/storage"
)

func main() {
	if remoteRunnerRequested(os.Args[1:]) {
		if err := runRemoteRunner(os.Args[1:]); err != nil &&
			!errors.Is(err, context.Canceled) {
			log.Fatal(err)
		}
		return
	}
	server, ephemeralSessions, err := newServer(os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}
	if ephemeralSessions {
		log.Print("session cookie keys are ephemeral; configure GITONE_SESSION_HASH_KEY and GITONE_SESSION_BLOCK_KEY to preserve browser sessions across restarts")
	}
	log.Printf("GitOne listening on %s", server.Addr)
	log.Fatal(server.ListenAndServe())
}

func newServer(args []string) (*http.Server, bool, error) {
	flags := flag.NewFlagSet("gitone", flag.ContinueOnError)
	root := flags.String("root", "./data", "storage root")
	listen := flags.String("listen", ":8080", "listen address")
	publicURL := flags.String("public-url", "http://localhost:8080", "public base URL")
	runnerEnabled := flags.Bool("runner", false, "enable container builds from .gitone.json")
	runnerToken := flags.String(
		"runner-token",
		os.Getenv("GITONE_RUNNER_TOKEN"),
		"shared token enabling the remote runner API",
	)
	runnerCommand := flags.String("runner-command", "docker", "Docker-compatible runner command")
	runnerWorkers := flags.Int("runner-workers", 1, "number of concurrent builds")
	if err := flags.Parse(args); err != nil {
		return nil, false, err
	}
	ldapConfig, err := auth.LDAPConfigFromEnvironment()
	if err != nil {
		return nil, false, err
	}
	if ldapConfig.URL == "" {
		return nil, false, errors.New("LDAP_URL is required")
	}
	directory, err := auth.NewLDAPAuthenticator(ldapConfig)
	if err != nil {
		return nil, false, err
	}
	parsedPublicURL, err := url.Parse(*publicURL)
	if err != nil {
		return nil, false, err
	}
	sessionConfig, ephemeralSessions, err := auth.SessionConfigFromEnvironment(
		parsedPublicURL.Scheme == "https",
	)
	if err != nil {
		return nil, false, err
	}
	sessions, err := auth.NewSessionManager(sessionConfig)
	if err != nil {
		return nil, false, err
	}
	var buildRunner *runner.Runner
	var coordinator *runner.Coordinator
	if *runnerEnabled && *runnerToken != "" {
		return nil, false, errors.New("embedded and remote runners cannot be enabled together")
	}
	if *runnerEnabled {
		buildRunner, err = runner.New(runner.Config{
			Storage:  storage.Store{Root: *root},
			State:    runner.NewStore(*root),
			Executor: runner.ContainerExecutor{Command: *runnerCommand},
			Workers:  *runnerWorkers,
		})
		if err != nil {
			return nil, false, err
		}
	}
	if *runnerToken != "" {
		coordinator, err = runner.NewCoordinator(runner.CoordinatorConfig{
			Storage: storage.Store{Root: *root},
			State:   runner.NewStore(*root),
		})
		if err != nil {
			return nil, false, err
		}
	}
	h := app.New(app.Config{
		Root:        *root,
		PublicURL:   *publicURL,
		Directory:   directory,
		Sessions:    sessions,
		Runner:      buildRunner,
		Coordinator: coordinator,
		RunnerToken: *runnerToken,
	})
	server := &http.Server{
		Addr:              *listen,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	if buildRunner != nil {
		server.RegisterOnShutdown(buildRunner.Close)
	}
	return server, ephemeralSessions, nil
}

func remoteRunnerRequested(args []string) bool {
	for _, argument := range args {
		if argument == "-runner-remote" || argument == "--runner-remote" {
			return true
		}
	}
	return false
}

func runRemoteRunner(args []string) error {
	flags := flag.NewFlagSet("gitone-runner", flag.ContinueOnError)
	_ = flags.Bool("runner-remote", false, "run as a remote build worker")
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
		return err
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
		return err
	}
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()
	log.Printf("GitOne remote runner %s polling %s", *runnerID, *serverURL)
	return remote.Run(ctx)
}
