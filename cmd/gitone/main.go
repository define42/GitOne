package main

import (
	"errors"
	"flag"
	"log"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/define42/GitOne/internal/auth"
	app "github.com/define42/GitOne/internal/server"
)

func main() {
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
	h := app.New(app.Config{
		Root:      *root,
		PublicURL: *publicURL,
		Directory: directory,
		Sessions:  sessions,
	})
	return &http.Server{
		Addr:              *listen,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}, ephemeralSessions, nil
}
