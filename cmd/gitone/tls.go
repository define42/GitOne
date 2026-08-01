package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/caddyserver/certmagic"
)

type transportMode string

const (
	transportUnencrypted transportMode = "unencrypted"
	transportACME        transportMode = "acme"
)

type transportConfig struct {
	mode         transportMode
	domains      []string
	acmeEmail    string
	acmeCA       string
	acmeRoots    *x509.CertPool
	acmeRootFile string
	acmeStorage  string
}

func transportConfigFromEnvironment(root string) (transportConfig, error) {
	mode := transportMode(strings.ToLower(environmentOrDefault(
		"GITONE_TLS_MODE",
		string(transportUnencrypted),
	)))
	config := transportConfig{
		mode:         mode,
		domains:      commaSeparatedEnvironment("GITONE_TLS_DOMAINS"),
		acmeEmail:    strings.TrimSpace(os.Getenv("GITONE_ACME_EMAIL")),
		acmeCA:       strings.TrimSpace(os.Getenv("GITONE_ACME_DIRECTORY")),
		acmeRootFile: strings.TrimSpace(os.Getenv("GITONE_ACME_CA_ROOT")),
		acmeStorage:  strings.TrimSpace(os.Getenv("GITONE_ACME_STORAGE")),
	}

	switch config.mode {
	case transportUnencrypted:
		return config, nil
	case transportACME:
		if len(config.domains) == 0 {
			return transportConfig{}, errors.New(
				"GITONE_TLS_MODE=acme requires GITONE_TLS_DOMAINS",
			)
		}
		for _, domain := range config.domains {
			if !certificateSubjectValid(domain) {
				return transportConfig{}, fmt.Errorf(
					"invalid ACME certificate domain %q",
					domain,
				)
			}
			if strings.Contains(domain, "*") {
				return transportConfig{}, fmt.Errorf(
					"ACME certificate domain %q is a wildcard; GitOne's TLS-ALPN-01 challenge does not support wildcard certificates",
					domain,
				)
			}
		}
		if config.acmeStorage == "" {
			config.acmeStorage = filepath.Join(root, "acme")
		}
		if config.acmeRootFile != "" {
			roots, err := certPoolFromPEM(config.acmeRootFile)
			if err != nil {
				return transportConfig{}, err
			}
			config.acmeRoots = roots
		}
		return config, nil
	default:
		return transportConfig{}, fmt.Errorf(
			"invalid GITONE_TLS_MODE %q (want unencrypted or acme)",
			config.mode,
		)
	}
}

func certificateSubjectValid(subject string) bool {
	if !certmagic.SubjectQualifiesForCert(subject) {
		return false
	}
	if net.ParseIP(subject) != nil {
		return true
	}
	return !strings.ContainsAny(subject, "/:@")
}

func (config transportConfig) validatePublicURL(publicURL *url.URL) error {
	if config.mode != transportACME {
		return nil
	}
	if publicURL.Scheme != "https" || publicURL.Hostname() == "" {
		return errors.New(
			"-public-url must be an absolute HTTPS URL when GITONE_TLS_MODE=acme",
		)
	}
	publicHost := strings.TrimSuffix(strings.ToLower(publicURL.Hostname()), ".")
	for _, domain := range config.domains {
		if strings.TrimSuffix(strings.ToLower(domain), ".") == publicHost {
			return nil
		}
	}
	return fmt.Errorf(
		"-public-url hostname %q must be included in GITONE_TLS_DOMAINS",
		publicURL.Hostname(),
	)
}

func (config transportConfig) protocol() string {
	if config.mode == transportACME {
		return "HTTPS"
	}
	return "HTTP"
}

func environmentOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func commaSeparatedEnvironment(key string) []string {
	values := make([]string, 0)
	seen := make(map[string]struct{})
	for _, raw := range strings.Split(os.Getenv(key), ",") {
		value := strings.TrimSpace(raw)
		canonical := strings.ToLower(value)
		if value == "" {
			continue
		}
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		values = append(values, value)
	}
	return values
}

func certPoolFromPEM(path string) (*x509.CertPool, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read ACME root CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(contents) {
		return nil, fmt.Errorf("no certificates found in ACME root CA file %s", path)
	}
	return pool, nil
}

type acmeManager struct {
	cache   *certmagic.Cache
	config  *certmagic.Config
	domains []string
}

func newACMEManager(config transportConfig, tlsALPNPort int) *acmeManager {
	storage := &certmagic.FileStorage{Path: config.acmeStorage}
	issuerTemplate := certmagic.ACMEIssuer{
		CA:                   config.acmeCA,
		Email:                config.acmeEmail,
		Agreed:               true,
		DisableHTTPChallenge: true,
		AltTLSALPNPort:       tlsALPNPort,
		TrustedRoots:         config.acmeRoots,
	}

	var magic *certmagic.Config
	ready := make(chan struct{})
	cache := certmagic.NewCache(certmagic.CacheOptions{
		GetConfigForCert: func(certmagic.Certificate) (*certmagic.Config, error) {
			// The cache starts its maintenance goroutine immediately. Do not let
			// that goroutine observe the configuration before it is fully wired.
			<-ready
			return magic, nil
		},
	})
	magic = certmagic.New(cache, certmagic.Config{Storage: storage})
	magic.Issuers = []certmagic.Issuer{
		certmagic.NewACMEIssuer(magic, issuerTemplate),
	}
	close(ready)

	return &acmeManager{
		cache:   cache,
		config:  magic,
		domains: config.domains,
	}
}

func (manager *acmeManager) start(ctx context.Context) error {
	if err := manager.config.ManageAsync(ctx, manager.domains); err != nil {
		return fmt.Errorf("manage ACME certificates: %w", err)
	}
	return nil
}

func (manager *acmeManager) tlsConfig() *tls.Config {
	config := manager.config.TLSConfig()
	config.MinVersion = tls.VersionTLS12
	config.NextProtos = append(
		[]string{"h2", "http/1.1"},
		config.NextProtos...,
	)
	return config
}

func (manager *acmeManager) stop() {
	manager.cache.Stop()
}

type configuredServer struct {
	*http.Server
	transport transportConfig
}

func (server *configuredServer) ListenAndServe() error {
	if server.transport.mode != transportACME {
		return server.Server.ListenAndServe()
	}

	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()

	// Claim the application socket before certificate management starts. The
	// CertMagic TLS-ALPN solver then recognizes that GitOne owns the challenge
	// port and serves challenges through the TLS config below instead of racing
	// the application for the listener.
	port, err := listenerPort(listener.Addr())
	if err != nil {
		return err
	}
	manager := newACMEManager(server.transport, port)
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
		manager.stop()
	}()
	if err = manager.start(ctx); err != nil {
		return err
	}

	server.TLSConfig = manager.tlsConfig()
	return server.Server.ServeTLS(listener, "", "")
}

func listenerPort(address net.Addr) (int, error) {
	_, rawPort, err := net.SplitHostPort(address.String())
	if err != nil {
		return 0, fmt.Errorf("determine TLS listener port: %w", err)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		return 0, fmt.Errorf("determine TLS listener port: %w", err)
	}
	return port, nil
}
