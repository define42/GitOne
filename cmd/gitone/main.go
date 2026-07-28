package main

import (
	"flag"
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/define42/GitOne/internal/auth"
	app "github.com/define42/GitOne/internal/server"
)

func main() {
	root := flag.String("root", "./data", "storage root")
	listen := flag.String("listen", ":8080", "listen address")
	publicURL := flag.String("public-url", "http://localhost:8080", "public base URL")
	flag.Parse()
	ldapConfig, err := auth.LDAPConfigFromEnvironment()
	if err != nil {
		log.Fatal(err)
	}
	if ldapConfig.URL == "" {
		log.Fatal("LDAP_URL is required")
	}
	directory, err := auth.NewLDAPAuthenticator(ldapConfig)
	if err != nil {
		log.Fatal(err)
	}
	parsedPublicURL, err := url.Parse(*publicURL)
	if err != nil {
		log.Fatal(err)
	}
	sessionConfig, ephemeralSessions, err := auth.SessionConfigFromEnvironment(
		parsedPublicURL.Scheme == "https",
	)
	if err != nil {
		log.Fatal(err)
	}
	sessions, err := auth.NewSessionManager(sessionConfig)
	if err != nil {
		log.Fatal(err)
	}
	if ephemeralSessions {
		log.Print("session cookie keys are ephemeral; configure GITONE_SESSION_HASH_KEY and GITONE_SESSION_BLOCK_KEY to preserve browser sessions across restarts")
	}
	h := app.New(app.Config{
		Root:      *root,
		PublicURL: *publicURL,
		Directory: directory,
		Sessions:  sessions,
	})
	s := &http.Server{Addr: *listen, Handler: h, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	log.Printf("GitOne listening on %s", *listen)
	log.Fatal(s.ListenAndServe())
}
