package main

import (
	"crypto/tls"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/caddyserver/certmagic"
)

type staticAddress string

func (address staticAddress) Network() string { return "tcp" }
func (address staticAddress) String() string  { return string(address) }

func clearTLSEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"GITONE_TLS_MODE",
		"GITONE_TLS_DOMAINS",
		"GITONE_ACME_EMAIL",
		"GITONE_ACME_DIRECTORY",
		"GITONE_ACME_CA_ROOT",
		"GITONE_ACME_STORAGE",
	} {
		t.Setenv(key, "")
	}
}

func TestTransportConfigFromEnvironment(t *testing.T) {
	t.Run("defaults to unencrypted", func(t *testing.T) {
		clearTLSEnvironment(t)
		config, err := transportConfigFromEnvironment(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if config.mode != transportUnencrypted || config.protocol() != "HTTP" {
			t.Fatalf("unexpected default transport: %#v", config)
		}
	})

	t.Run("complete ACME configuration", func(t *testing.T) {
		clearTLSEnvironment(t)
		root := t.TempDir()
		storage := filepath.Join(t.TempDir(), "certificates")
		rootCA := filepath.Join(t.TempDir(), "root.pem")
		certificate, err := os.ReadFile("../../testldap/cert.pem")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(rootCA, certificate, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GITONE_TLS_MODE", " ACME ")
		t.Setenv("GITONE_TLS_DOMAINS", " git.example,api.example,GIT.EXAMPLE, ")
		t.Setenv("GITONE_ACME_EMAIL", " operator@example.com ")
		t.Setenv("GITONE_ACME_DIRECTORY", " https://acme.example/directory ")
		t.Setenv("GITONE_ACME_CA_ROOT", " "+rootCA+" ")
		t.Setenv("GITONE_ACME_STORAGE", " "+storage+" ")

		config, err := transportConfigFromEnvironment(root)
		if err != nil {
			t.Fatal(err)
		}
		if config.mode != transportACME ||
			!slices.Equal(config.domains, []string{"git.example", "api.example"}) ||
			config.acmeEmail != "operator@example.com" ||
			config.acmeCA != "https://acme.example/directory" ||
			config.acmeRootFile != rootCA || config.acmeRoots == nil ||
			config.acmeStorage != storage {
			t.Fatalf("unexpected ACME transport: %#v", config)
		}
	})

	t.Run("default ACME storage", func(t *testing.T) {
		clearTLSEnvironment(t)
		root := t.TempDir()
		t.Setenv("GITONE_TLS_MODE", "acme")
		t.Setenv("GITONE_TLS_DOMAINS", "git.example")
		config, err := transportConfigFromEnvironment(root)
		if err != nil {
			t.Fatal(err)
		}
		if config.acmeStorage != filepath.Join(root, "acme") {
			t.Fatalf("default ACME storage = %q", config.acmeStorage)
		}
	})

	t.Run("malformed private CA root", func(t *testing.T) {
		clearTLSEnvironment(t)
		rootCA := filepath.Join(t.TempDir(), "root.pem")
		if err := os.WriteFile(rootCA, []byte("not a PEM certificate"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GITONE_TLS_MODE", "acme")
		t.Setenv("GITONE_TLS_DOMAINS", "git.example")
		t.Setenv("GITONE_ACME_CA_ROOT", rootCA)

		_, err := transportConfigFromEnvironment(t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "no certificates found") {
			t.Fatalf("transport configuration error = %v, want malformed root CA error", err)
		}
	})

	for _, test := range []struct {
		name      string
		mode      string
		domains   string
		rootCA    string
		wantError string
	}{
		{
			name:      "unknown mode",
			mode:      "automatic",
			wantError: "invalid GITONE_TLS_MODE",
		},
		{
			name:      "missing domains",
			mode:      "acme",
			wantError: "requires GITONE_TLS_DOMAINS",
		},
		{
			name:      "invalid domain",
			mode:      "acme",
			domains:   "https://git.example",
			wantError: "invalid ACME certificate domain",
		},
		{
			name:      "wildcard domain",
			mode:      "acme",
			domains:   "*.example.com",
			wantError: "does not support wildcard",
		},
		{
			name:      "missing private CA root",
			mode:      "acme",
			domains:   "git.example",
			rootCA:    "missing.pem",
			wantError: "read ACME root CA",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			clearTLSEnvironment(t)
			t.Setenv("GITONE_TLS_MODE", test.mode)
			t.Setenv("GITONE_TLS_DOMAINS", test.domains)
			t.Setenv("GITONE_ACME_CA_ROOT", test.rootCA)
			_, err := transportConfigFromEnvironment(t.TempDir())
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("transport configuration error = %v, want containing %q", err, test.wantError)
			}
		})
	}
}

func TestConfiguredServerUnencryptedListenFailure(t *testing.T) {
	server := &configuredServer{
		Server: &http.Server{Addr: "127.0.0.1:8080:extra"},
		transport: transportConfig{
			mode: transportUnencrypted,
		},
	}

	err := server.ListenAndServe()
	if err == nil || !strings.Contains(err.Error(), "too many colons") {
		t.Fatalf("ListenAndServe error = %v, want invalid listen address", err)
	}
}

func TestConfiguredServerACMEListenFailure(t *testing.T) {
	server := &configuredServer{
		Server: &http.Server{Addr: "127.0.0.1:8080:extra"},
		transport: transportConfig{
			mode: transportACME,
		},
	}

	err := server.ListenAndServe()
	if err == nil || !strings.Contains(err.Error(), "too many colons") {
		t.Fatalf("ListenAndServe error = %v, want invalid listen address", err)
	}
}

func TestListenerPort(t *testing.T) {
	tests := []struct {
		name      string
		address   net.Addr
		want      int
		wantError bool
	}{
		{
			name:    "valid",
			address: staticAddress("127.0.0.1:8443"),
			want:    8443,
		},
		{
			name:      "malformed address",
			address:   staticAddress("127.0.0.1"),
			wantError: true,
		},
		{
			name:      "non-numeric port",
			address:   staticAddress("127.0.0.1:https"),
			wantError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := listenerPort(test.address)
			if test.wantError {
				if err == nil || !strings.Contains(err.Error(), "determine TLS listener port") {
					t.Fatalf("listenerPort error = %v, want port error", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("listenerPort = %d, want %d", got, test.want)
			}
		})
	}
}

func TestTransportConfigValidatesACMEPublicURL(t *testing.T) {
	config := transportConfig{
		mode:    transportACME,
		domains: []string{"git.example", "api.example"},
	}
	for _, rawURL := range []string{
		"https://git.example",
		"https://GIT.EXAMPLE:8443/base",
	} {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			t.Fatal(err)
		}
		if err = config.validatePublicURL(parsed); err != nil {
			t.Fatalf("validate %s: %v", rawURL, err)
		}
	}
}

func TestACMEManagerConfiguresCertMagicAndTLS(t *testing.T) {
	storagePath := filepath.Join(t.TempDir(), "acme")
	manager := newACMEManager(transportConfig{
		mode:        transportACME,
		domains:     []string{"git.example"},
		acmeEmail:   "operator@example.com",
		acmeCA:      "https://acme.example/directory",
		acmeStorage: storagePath,
	}, 8443)
	defer manager.stop()

	storage, ok := manager.config.Storage.(*certmagic.FileStorage)
	if !ok || storage.Path != storagePath {
		t.Fatalf("CertMagic storage = %#v", manager.config.Storage)
	}
	if len(manager.config.Issuers) != 1 {
		t.Fatalf("CertMagic issuers = %#v", manager.config.Issuers)
	}
	issuer, ok := manager.config.Issuers[0].(*certmagic.ACMEIssuer)
	if !ok || issuer.CA != "https://acme.example/directory" ||
		issuer.Email != "operator@example.com" || !issuer.Agreed ||
		!issuer.DisableHTTPChallenge || issuer.AltTLSALPNPort != 8443 {
		t.Fatalf("CertMagic ACME issuer = %#v", manager.config.Issuers[0])
	}

	tlsConfig := manager.tlsConfig()
	if tlsConfig.MinVersion != tls.VersionTLS12 || tlsConfig.GetCertificate == nil {
		t.Fatalf("TLS config = %#v", tlsConfig)
	}
	for _, protocol := range []string{"h2", "http/1.1", "acme-tls/1"} {
		if !slices.Contains(tlsConfig.NextProtos, protocol) {
			t.Errorf("TLS NextProtos %v does not contain %q", tlsConfig.NextProtos, protocol)
		}
	}
}
