package auth

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-ldap/ldap/v3"
)

func TestLDAPConfigFromEnvironment(t *testing.T) {
	t.Setenv("LDAP_URL", "ldaps://ldap:389")
	t.Setenv("LDAP_BASE_DN", "dc=glauth,dc=com")
	t.Setenv("LDAP_USER_DOMAIN", "example.com")
	t.Setenv("LDAP_USER_FILTER", "(mail=%s)")
	t.Setenv("LDAP_CANONICAL_ATTRIBUTE", "entryUUID")
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
		config.CanonicalAttribute != "entryUUID" ||
		!config.SkipTLSVerify ||
		!config.StartTLS ||
		config.ConnectionTimeout != 3*time.Second {
		t.Fatalf("unexpected LDAP environment config: %#v", config)
	}
}

func TestLDAPAuthenticatorDefaultsAndEscapesUserFilter(t *testing.T) {
	authenticator, err := NewLDAPAuthenticator(LDAPConfig{
		URL:        "ldaps://ldap.example:636",
		BaseDN:     "dc=example,dc=com",
		UserDomain: "example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if authenticator.config.UserFilter != "(mail=%s)" ||
		authenticator.config.CanonicalAttribute != "mail" ||
		authenticator.tlsConfig.ServerName != "ldap.example" {
		t.Fatalf("unexpected LDAP defaults: %#v", authenticator.config)
	}
	filter := authenticator.userFilter(`alice@example.com*)(|(mail=*))`)
	if strings.Contains(filter, `alice@example.com*)(|`) ||
		filter != `(mail=alice@example.com\2a\29\28|\28mail=\2a\29\29)` {
		t.Fatalf("LDAP username was not escaped: %q", filter)
	}
}

func TestLDAPAuthenticatorRequiresFullDomainLogin(t *testing.T) {
	authenticator, err := NewLDAPAuthenticator(LDAPConfig{
		URL:        "ldaps://ldap.example:636",
		BaseDN:     "dc=example,dc=com",
		UserDomain: "example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		input string
		want  string
	}{
		{input: "alice@example.com", want: "alice@example.com"},
		{input: " Alice@EXAMPLE.COM ", want: "alice@example.com"},
	} {
		identifier, identifierErr := authenticator.loginIdentifier(test.input)
		if identifierErr != nil || identifier != test.want {
			t.Fatalf("loginIdentifier(%q) = %q, %v", test.input, identifier, identifierErr)
		}
	}
	for _, input := range []string{
		"alice",
		"alice@",
		"@example.com",
		"alice@@example.com",
		"alice@other.example",
		"alice smith@example.com",
	} {
		if identifier, identifierErr := authenticator.loginIdentifier(input); identifierErr == nil {
			t.Fatalf("loginIdentifier(%q) accepted as %q", input, identifier)
		}
	}
}

func TestLDAPAuthenticatorReturnsCanonicalEntryIdentity(t *testing.T) {
	authenticator, err := NewLDAPAuthenticator(LDAPConfig{
		URL:                "ldaps://ldap.example:636",
		BaseDN:             "dc=example,dc=com",
		UserDomain:         "example.com",
		CanonicalAttribute: "mail",
	})
	if err != nil {
		t.Fatal(err)
	}
	entry := ldap.NewEntry(
		"cn=alice,dc=example,dc=com",
		map[string][]string{"MAIL": {"Alice@EXAMPLE.COM"}},
	)
	canonical, err := authenticator.canonicalIdentity(entry)
	if err != nil {
		t.Fatal(err)
	}
	if canonical != "alice@example.com" {
		t.Fatalf("canonical identity = %q", canonical)
	}
	for _, values := range [][]string{
		nil,
		{""},
		{"alice@example.com", "alias@example.com"},
		{"alice@other.example"},
	} {
		entry = ldap.NewEntry(
			"cn=alice,dc=example,dc=com",
			map[string][]string{"mail": values},
		)
		if _, err = authenticator.canonicalIdentity(entry); err == nil {
			t.Fatalf("invalid canonical mail values were accepted: %#v", values)
		}
	}

	authenticator.config.CanonicalAttribute = "entryUUID"
	entry = ldap.NewEntry(
		"cn=alice,dc=example,dc=com",
		map[string][]string{"entryUUID": {"4f31e157-1d7a-4b2f-a08a-4754cb47c539"}},
	)
	canonical, err = authenticator.canonicalIdentity(entry)
	if err != nil {
		t.Fatal(err)
	}
	if canonical != "4f31e157-1d7a-4b2f-a08a-4754cb47c539" {
		t.Fatalf("canonical immutable identity = %q", canonical)
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
		{URL: "http://ldap.example", BaseDN: "dc=example,dc=com", UserDomain: "example.com"},
		{URL: "ldaps://ldap.example", BaseDN: "dc=example,dc=com"},
		{
			URL:        "ldaps://ldap.example",
			BaseDN:     "dc=example,dc=com",
			UserDomain: "example.com",
			UserFilter: "(cn=alice)",
		},
		{URL: "ldaps://ldap.example", BaseDN: "dc=example,dc=com", UserDomain: "@example.com"},
		{URL: "ldaps://ldap.example", BaseDN: "dc=example,dc=com", UserDomain: "bad@example.com"},
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
		URL:        "ldap://directory.example:389",
		UserDomain: "example.com",
	}); err == nil {
		t.Fatal("empty LDAP base DN was accepted")
	}
	if _, err := NewLDAPAuthenticator(LDAPConfig{
		URL:        "ldap://directory.example:389",
		BaseDN:     "dc=example,dc=com",
		UserDomain: "example.com",
		CAFile:     filepath.Join(t.TempDir(), "missing.pem"),
	}); err == nil || !strings.Contains(err.Error(), "read LDAP CA file") {
		t.Fatalf("missing CA file returned %v", err)
	}
	invalidCA := filepath.Join(t.TempDir(), "invalid.pem")
	if err := os.WriteFile(invalidCA, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLDAPAuthenticator(LDAPConfig{
		URL:        "ldap://directory.example:389",
		BaseDN:     "dc=example,dc=com",
		UserDomain: "example.com",
		CAFile:     invalidCA,
	}); err == nil || !strings.Contains(err.Error(), "does not contain a certificate") {
		t.Fatalf("invalid CA file returned %v", err)
	}
}

func TestLDAPAuthenticateRejectsInvalidInputAndUnavailableDirectory(t *testing.T) {
	authenticator, err := NewLDAPAuthenticator(LDAPConfig{
		URL:               "ldap://127.0.0.1:1",
		BaseDN:            "dc=example,dc=com",
		UserDomain:        "example.com",
		ConnectionTimeout: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, credentials := range [][2]string{
		{"", "secret"},
		{"alice@example.com", ""},
		{"alice", "secret"},
		{"alice@other.example", "secret"},
	} {
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
	if _, err = authenticator.Authenticate(cancelled, "alice@example.com", "secret"); err == nil {
		t.Fatal("cancelled LDAP authentication proceeded")
	}
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err = authenticator.Authenticate(expired, "alice@example.com", "secret"); err == nil {
		t.Fatal("expired LDAP authentication proceeded")
	}
	if _, err = authenticator.Authenticate(
		context.Background(),
		"alice@example.com",
		"secret",
	); err == nil || !strings.Contains(err.Error(), "connect to LDAP") {
		t.Fatalf("unavailable LDAP directory returned %v", err)
	}
	shortDeadline, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if _, err = authenticator.Authenticate(
		shortDeadline,
		"alice@example.com",
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
	username, err := authenticator.Authenticate(
		context.Background(),
		"johndoe@example.com",
		"dogood",
	)
	if err != nil {
		t.Fatalf("authenticate fixture user: %v", err)
	}
	if username != "johndoe@example.com" {
		t.Fatalf("unexpected canonical username %q", username)
	}
	if _, err = authenticator.Authenticate(
		context.Background(),
		"johndoe@example.com",
		"wrong",
	); err == nil {
		t.Fatal("invalid fixture password authenticated")
	}
}
