package auth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-ldap/ldap/v3"
)

const (
	ldapUsernamePlaceholder         = "%s"
	defaultLDAPCanonicalAttribute   = "mail"
	invalidLDAPCredentialsErrorText = "invalid LDAP credentials"
)

type LDAPConfig struct {
	URL                string
	BaseDN             string
	UserDomain         string
	UserFilter         string
	CanonicalAttribute string
	CAFile             string
	SkipTLSVerify      bool
	StartTLS           bool
	ConnectionTimeout  time.Duration
}

type LDAPAuthenticator struct {
	config    LDAPConfig
	tlsConfig *tls.Config
}

func LDAPConfigFromEnvironment() (LDAPConfig, error) {
	config := LDAPConfig{
		URL:        strings.TrimSpace(os.Getenv("LDAP_URL")),
		BaseDN:     strings.TrimSpace(os.Getenv("LDAP_BASE_DN")),
		UserDomain: strings.TrimSpace(os.Getenv("LDAP_USER_DOMAIN")),
		UserFilter: strings.TrimSpace(os.Getenv("LDAP_USER_FILTER")),
		CanonicalAttribute: strings.TrimSpace(
			os.Getenv("LDAP_CANONICAL_ATTRIBUTE"),
		),
		CAFile: strings.TrimSpace(os.Getenv("LDAP_CA_FILE")),
	}
	if value := strings.TrimSpace(os.Getenv("LDAP_SKIP_TLS_VERIFY")); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return config, fmt.Errorf("LDAP_SKIP_TLS_VERIFY: %w", err)
		}
		config.SkipTLSVerify = parsed
	}
	if value := strings.TrimSpace(os.Getenv("LDAP_STARTTLS")); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return config, fmt.Errorf("LDAP_STARTTLS: %w", err)
		}
		config.StartTLS = parsed
	}
	if value := strings.TrimSpace(os.Getenv("LDAP_CONNECTION_TIMEOUT")); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil {
			return config, fmt.Errorf("LDAP_CONNECTION_TIMEOUT: %w", err)
		}
		config.ConnectionTimeout = parsed
	}
	return config, nil
}

func NewLDAPAuthenticator(config LDAPConfig) (*LDAPAuthenticator, error) {
	config.URL = strings.TrimSpace(config.URL)
	config.BaseDN = strings.TrimSpace(config.BaseDN)
	config.UserDomain = strings.ToLower(strings.TrimSpace(config.UserDomain))
	config.UserFilter = strings.TrimSpace(config.UserFilter)
	config.CanonicalAttribute = strings.TrimSpace(config.CanonicalAttribute)
	if config.URL == "" {
		return nil, errors.New("LDAP URL is required")
	}
	parsedURL, err := url.Parse(config.URL)
	if err != nil || (parsedURL.Scheme != "ldap" && parsedURL.Scheme != "ldaps") ||
		parsedURL.Host == "" {
		return nil, errors.New("LDAP URL must use ldap:// or ldaps://")
	}
	if config.BaseDN == "" {
		return nil, errors.New("LDAP base DN is required")
	}
	if config.UserDomain == "" {
		return nil, errors.New("LDAP user domain is required")
	}
	if strings.ContainsAny(config.UserDomain, "@ \t\r\n") {
		return nil, errors.New("LDAP user domain must be a domain name")
	}
	if config.UserFilter == "" {
		config.UserFilter = "(mail=" + ldapUsernamePlaceholder + ")"
	}
	if !strings.Contains(config.UserFilter, ldapUsernamePlaceholder) {
		return nil, fmt.Errorf("LDAP user filter must contain %s", ldapUsernamePlaceholder)
	}
	if config.CanonicalAttribute == "" {
		config.CanonicalAttribute = defaultLDAPCanonicalAttribute
	}
	if config.ConnectionTimeout <= 0 {
		config.ConnectionTimeout = 5 * time.Second
	}

	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: parsedURL.Hostname(),
	}
	if config.SkipTLSVerify {
		tlsConfig.InsecureSkipVerify = true
	}
	if config.CAFile != "" {
		pem, readErr := os.ReadFile(config.CAFile)
		if readErr != nil {
			return nil, fmt.Errorf("read LDAP CA file: %w", readErr)
		}
		roots, rootErr := x509.SystemCertPool()
		if rootErr != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, errors.New("LDAP CA file does not contain a certificate")
		}
		tlsConfig.RootCAs = roots
	}
	return &LDAPAuthenticator{config: config, tlsConfig: tlsConfig}, nil
}

func (a *LDAPAuthenticator) Authenticate(
	ctx context.Context,
	username string,
	password string,
) (string, error) {
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return "", errors.New("LDAP username and password are required")
	}
	identifier, err := a.loginIdentifier(username)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	timeout := a.config.ConnectionTimeout
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return "", context.DeadlineExceeded
		}
		if remaining < timeout {
			timeout = remaining
		}
	}
	connection, err := ldap.DialURL(
		a.config.URL,
		ldap.DialWithDialer(&net.Dialer{Timeout: timeout}),
		ldap.DialWithTLSConfig(a.tlsConfig.Clone()),
	)
	if err != nil {
		return "", fmt.Errorf("connect to LDAP: %w", err)
	}
	defer func() {
		_ = connection.Close()
	}()
	connection.SetTimeout(timeout)

	if a.config.StartTLS && parsedLDAPScheme(a.config.URL) == "ldap" {
		if err = connection.StartTLS(a.tlsConfig.Clone()); err != nil {
			return "", fmt.Errorf("start LDAP TLS: %w", err)
		}
	}
	if err = connection.Bind(identifier, password); err != nil {
		return "", errors.New(invalidLDAPCredentialsErrorText)
	}

	filter := a.userFilter(identifier)
	searchTimeoutSeconds := int(timeout.Seconds())
	if searchTimeoutSeconds < 1 {
		searchTimeoutSeconds = 1
	}
	result, err := connection.Search(ldap.NewSearchRequest(
		a.config.BaseDN,
		ldap.ScopeWholeSubtree,
		ldap.NeverDerefAliases,
		2,
		searchTimeoutSeconds,
		false,
		filter,
		[]string{a.config.CanonicalAttribute},
		nil,
	))
	if err != nil {
		return "", fmt.Errorf("search LDAP user: %w", err)
	}
	if len(result.Entries) != 1 {
		return "", errors.New("LDAP user was not uniquely identified")
	}
	return a.canonicalIdentity(result.Entries[0])
}

func (a *LDAPAuthenticator) loginIdentifier(username string) (string, error) {
	username = strings.TrimSpace(username)
	at := strings.LastIndexByte(username, '@')
	if at <= 0 ||
		at != strings.IndexByte(username, '@') ||
		at == len(username)-1 ||
		strings.ContainsAny(username, " \t\r\n") ||
		!strings.EqualFold(username[at+1:], a.config.UserDomain) {
		return "", errors.New(invalidLDAPCredentialsErrorText)
	}
	return strings.ToLower(username[:at]) + "@" + strings.ToLower(a.config.UserDomain), nil
}

func (a *LDAPAuthenticator) canonicalIdentity(entry *ldap.Entry) (string, error) {
	values := entry.GetEqualFoldAttributeValues(a.config.CanonicalAttribute)
	if len(values) != 1 {
		return "", errors.New("LDAP canonical identity was not uniquely identified")
	}
	canonical := strings.TrimSpace(values[0])
	if canonical == "" || strings.ContainsAny(canonical, "\x00\r\n") {
		return "", errors.New("LDAP canonical identity is invalid")
	}
	if strings.EqualFold(a.config.CanonicalAttribute, "mail") {
		return a.loginIdentifier(canonical)
	}
	return canonical, nil
}

func (a *LDAPAuthenticator) userFilter(identifier string) string {
	return strings.ReplaceAll(
		a.config.UserFilter,
		ldapUsernamePlaceholder,
		ldap.EscapeFilter(identifier),
	)
}

func parsedLDAPScheme(value string) string {
	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}
	return parsed.Scheme
}
