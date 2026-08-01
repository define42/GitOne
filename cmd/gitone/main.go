package main

import (
	"errors"
	"flag"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/define42/GitOne/internal/auth"
	"github.com/define42/GitOne/internal/fipsmode"
	"github.com/define42/GitOne/internal/runner"
	app "github.com/define42/GitOne/internal/server"
	"github.com/define42/GitOne/internal/storage"
)

func main() {
	fipsmode.Must()

	server, ephemeralSessions, err := newServer(os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}
	if ephemeralSessions {
		log.Print("session cookie keys are ephemeral; configure GITONE_SESSION_HASH_KEY and GITONE_SESSION_BLOCK_KEY to preserve browser sessions across restarts")
	}
	log.Printf("GitOne listening with %s on %s", server.transport.protocol(), server.Addr)
	log.Fatal(server.ListenAndServe())
}

func newServer(args []string) (*configuredServer, bool, error) {
	flags := flag.NewFlagSet("gitone", flag.ContinueOnError)
	root := flags.String("root", "./data", "storage root")
	listen := flags.String("listen", ":8080", "listen address")
	publicURL := flags.String("public-url", "http://localhost:8080", "public base URL")
	runnerToken := flags.String(
		"runner-token",
		os.Getenv("GITONE_RUNNER_TOKEN"),
		"shared token enabling the remote runner API",
	)
	importAllowlist := flags.String(
		"import-allowlist",
		os.Getenv("GITONE_IMPORT_ALLOWLIST"),
		"comma-separated exact hostnames, IP addresses, or CIDRs allowed for remote imports",
	)
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
	transport, err := transportConfigFromEnvironment(*root)
	if err != nil {
		return nil, false, err
	}
	if err = transport.validatePublicURL(parsedPublicURL); err != nil {
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
	importNetworkPolicy, err := storage.NewImportNetworkPolicy(
		strings.Split(*importAllowlist, ","),
	)
	if err != nil {
		return nil, false, err
	}
	var coordinator *runner.Coordinator
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
		Root:                *root,
		PublicURL:           *publicURL,
		Directory:           directory,
		Sessions:            sessions,
		Coordinator:         coordinator,
		RunnerToken:         *runnerToken,
		ImportNetworkPolicy: importNetworkPolicy,
	})
	server := &configuredServer{
		Server: &http.Server{
			Addr:              *listen,
			Handler:           h,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       2 * time.Minute,
		},
		transport: transport,
	}
	return server, ephemeralSessions, nil
}
