package auth

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLDAPConfigFromEnvironment(t *testing.T) {
	t.Setenv("LDAP_URL", "ldaps://ldap:389")
	t.Setenv("LDAP_BASE_DN", "dc=glauth,dc=com")
	t.Setenv("LDAP_USER_DOMAIN", "example.com")
	t.Setenv("LDAP_USER_FILTER", "(mail=%s)")
	t.Setenv("LDAP_SKIP_TLS_VERIFY", "true")
	t.Setenv("LDAP_STARTTLS", "true")
	t.Setenv("LDAP_CONNECTION_TIMEOUT", "3s")

	config, err := LDAPConfigFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if config.URL != "ldaps://ldap:389" ||
		config.BaseDN != "dc=glauth,dc=com" ||
		config.UserDomain != "example.com" ||
		config.UserFilter != "(mail=%s)" ||
		!config.SkipTLSVerify ||
		!config.StartTLS ||
		config.ConnectionTimeout != 3*time.Second {
		t.Fatalf("unexpected LDAP environment config: %#v", config)
	}
}

func TestLDAPAuthenticatorDefaultsAndEscapesUserFilter(t *testing.T) {
	authenticator, err := NewLDAPAuthenticator(LDAPConfig{
		URL:    "ldaps://ldap.example:636",
		BaseDN: "dc=example,dc=com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if authenticator.config.UserFilter != "(mail=%s)" ||
		authenticator.tlsConfig.ServerName != "ldap.example" {
		t.Fatalf("unexpected LDAP defaults: %#v", authenticator.config)
	}
	filter := authenticator.userFilter(`alice@example.com*)(|(mail=*))`)
	if strings.Contains(filter, `alice@example.com*)(|`) ||
		filter != `(mail=alice@example.com\2a\29\28|\28mail=\2a\29\29)` {
		t.Fatalf("LDAP username was not escaped: %q", filter)
	}
}

func TestLDAPAuthenticatorBuildsLoginIdentifier(t *testing.T) {
	authenticator, err := NewLDAPAuthenticator(LDAPConfig{
		URL:        "ldaps://ldap.example:636",
		BaseDN:     "dc=example,dc=com",
		UserDomain: "@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if identifier := authenticator.loginIdentifier("alice"); identifier != "alice@example.com" {
		t.Fatalf("unexpected login identifier %q", identifier)
	}
	if identifier := authenticator.loginIdentifier("bob@other.example"); identifier != "bob@other.example" {
		t.Fatalf("qualified login identifier changed to %q", identifier)
	}
}

func TestParsedLDAPScheme(t *testing.T) {
	for _, test := range []struct {
		value, want string
	}{
		{value: "ldap://directory.example:389", want: "ldap"},
		{value: "ldaps://directory.example:636", want: "ldaps"},
		{value: "://invalid", want: ""},
	} {
		if got := parsedLDAPScheme(test.value); got != test.want {
			t.Fatalf("parsedLDAPScheme(%q) = %q, want %q", test.value, got, test.want)
		}
	}
}

func TestLDAPAuthenticatorRejectsIncompleteConfiguration(t *testing.T) {
	for _, config := range []LDAPConfig{
		{},
		{URL: "http://ldap.example", BaseDN: "dc=example,dc=com"},
		{URL: "ldaps://ldap.example", BaseDN: "dc=example,dc=com", UserFilter: "(cn=alice)"},
	} {
		if _, err := NewLDAPAuthenticator(config); err == nil {
			t.Fatalf("expected invalid LDAP config to fail: %#v", config)
		}
	}
}

func TestLDAPConfigurationRejectsInvalidEnvironmentAndCertificates(t *testing.T) {
	for _, test := range []struct {
		name, variable, value string
	}{
		{name: "skip TLS verification", variable: "LDAP_SKIP_TLS_VERIFY", value: "sometimes"},
		{name: "StartTLS", variable: "LDAP_STARTTLS", value: "sometimes"},
		{name: "connection timeout", variable: "LDAP_CONNECTION_TIMEOUT", value: "eventually"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(test.variable, test.value)
			if _, err := LDAPConfigFromEnvironment(); err == nil {
				t.Fatal("invalid LDAP environment value was accepted")
			}
		})
	}

	if _, err := NewLDAPAuthenticator(LDAPConfig{
		URL: "ldap://directory.example:389",
	}); err == nil {
		t.Fatal("empty LDAP base DN was accepted")
	}
	if _, err := NewLDAPAuthenticator(LDAPConfig{
		URL:    "ldap://directory.example:389",
		BaseDN: "dc=example,dc=com",
		CAFile: filepath.Join(t.TempDir(), "missing.pem"),
	}); err == nil || !strings.Contains(err.Error(), "read LDAP CA file") {
		t.Fatalf("missing CA file returned %v", err)
	}
	invalidCA := filepath.Join(t.TempDir(), "invalid.pem")
	if err := os.WriteFile(invalidCA, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLDAPAuthenticator(LDAPConfig{
		URL:    "ldap://directory.example:389",
		BaseDN: "dc=example,dc=com",
		CAFile: invalidCA,
	}); err == nil || !strings.Contains(err.Error(), "does not contain a certificate") {
		t.Fatalf("invalid CA file returned %v", err)
	}
}

func TestLDAPAuthenticateRejectsInvalidInputAndUnavailableDirectory(t *testing.T) {
	authenticator, err := NewLDAPAuthenticator(LDAPConfig{
		URL:               "ldap://127.0.0.1:1",
		BaseDN:            "dc=example,dc=com",
		ConnectionTimeout: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, credentials := range [][2]string{{"", "secret"}, {"alice", ""}} {
		if _, err = authenticator.Authenticate(
			context.Background(),
			credentials[0],
			credentials[1],
		); err == nil {
			t.Fatal("incomplete LDAP credentials were accepted")
		}
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = authenticator.Authenticate(cancelled, "alice", "secret"); err == nil {
		t.Fatal("cancelled LDAP authentication proceeded")
	}
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err = authenticator.Authenticate(expired, "alice", "secret"); err == nil {
		t.Fatal("expired LDAP authentication proceeded")
	}
	if _, err = authenticator.Authenticate(
		context.Background(),
		"alice",
		"secret",
	); err == nil || !strings.Contains(err.Error(), "connect to LDAP") {
		t.Fatalf("unavailable LDAP directory returned %v", err)
	}
	shortDeadline, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if _, err = authenticator.Authenticate(
		shortDeadline,
		"alice",
		"secret",
	); err == nil {
		t.Fatal("unavailable LDAP directory succeeded with a short deadline")
	}
}

func TestLDAPAuthenticatorIntegration(t *testing.T) {
	url := os.Getenv("LDAP_TEST_URL")
	if url == "" {
		t.Skip("LDAP_TEST_URL is not configured")
	}
	authenticator, err := NewLDAPAuthenticator(LDAPConfig{
		URL:           url,
		BaseDN:        "dc=glauth,dc=com",
		UserDomain:    "example.com",
		UserFilter:    "(mail=%s)",
		SkipTLSVerify: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	username, err := authenticator.Authenticate(context.Background(), "johndoe", "dogood")
	if err != nil {
		t.Fatalf("authenticate fixture user: %v", err)
	}
	if username != "johndoe" {
		t.Fatalf("unexpected canonical username %q", username)
	}
	if _, err = authenticator.Authenticate(context.Background(), "johndoe", "wrong"); err == nil {
		t.Fatal("invalid fixture password authenticated")
	}
}
