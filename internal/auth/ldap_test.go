package auth

import (
	"context"
	"os"
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
