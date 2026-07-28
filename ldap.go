//go:build ignore

// Package ldap authenticates users against the configured LDAP directory.
package ldap

import (
	"crypto/tls"
	"devboxgateway/internal/config"
	"devboxgateway/internal/types"
	"errors"
	"fmt"
	"strings"

	"github.com/go-ldap/ldap/v3"
)

// ErrEmptyPassword rejects a login with a zero-length password before any LDAP
// bind. Per RFC 4513 §5.1.2 a bind with a populated DN but an empty password is
// an "unauthenticated authentication" that many directories accept as an
// anonymous bind, which would let an empty password succeed against any
// account. The HTTP layer already rejects empty credentials, so this is the
// authoritative backstop guaranteeing the bypass cannot reappear via any caller.
var ErrEmptyPassword = errors.New("password must not be empty")

// Configured reports whether an LDAP directory is configured. When LDAP_URL is
// empty the gateway runs in local-users-only mode (see internal/localauth) and
// callers should skip LDAP entirely rather than attempt a dial.
func Configured(settings *config.SettingsType) bool {
	return strings.TrimSpace(settings.Get(config.LDAP_URL)) != ""
}

// AuthenticateAccess authenticates a user against LDAP and returns the gateway user model.
func AuthenticateAccess(username, password string, settings *config.SettingsType) (*types.User, error) {
	// Reject empty passwords before dialing or binding so an empty-password
	// anonymous bind can never authenticate a user. See ErrEmptyPassword.
	if password == "" {
		return nil, ErrEmptyPassword
	}

	conn, err := dialLDAP(settings)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()

	mail := loginIdentifier(username, settings)

	// Bind as the user using only the mail/UPN form.
	bindIDs := []string{mail}

	var bindErr error
	for _, id := range bindIDs {
		if id == "" {
			continue
		}
		if err := conn.Bind(id, password); err == nil {
			bindErr = nil
			break
		}
		bindErr = err
	}
	if bindErr != nil {
		return nil, fmt.Errorf("ldap bind failed: %w", bindErr)
	}

	userFilter := settings.Get(config.LDAP_USER_FILTER)
	baseDN := settings.Get(config.LDAP_BASE_DN)

	filter := fmt.Sprintf(userFilter, mail)
	searchReq := ldap.NewSearchRequest(
		baseDN,
		ldap.ScopeWholeSubtree,
		ldap.NeverDerefAliases, 1, 0, false,
		filter,
		nil,
		nil,
	)

	sr, err := conn.Search(searchReq)
	if err != nil {
		return nil, fmt.Errorf("ldap search: %w", err)
	}
	if len(sr.Entries) == 0 {
		return nil, fmt.Errorf("user %s not found", mail)
	}

	return types.NewUser(username)
}

func loginIdentifier(username string, settings *config.SettingsType) string {
	userMailDomain := settings.Get(config.LDAP_USER_DOMAIN)

	mail := username
	if !strings.Contains(username, "@") && userMailDomain != "" {
		domain := userMailDomain
		if !strings.HasPrefix(domain, "@") {
			domain = "@" + domain
		}
		mail = username + domain
	}

	return mail
}

func dialLDAP(settings *config.SettingsType) (*ldap.Conn, error) {
	// #nosec G402 -- skip TLS verification if configured
	ldapURL := settings.Get(config.LDAP_URL)
	insecureSkipVerify := settings.IsTrue(config.LDAP_SKIP_TLS_VERIFY)
	startTLS := settings.IsTrue(config.LDAP_STARTTLS)

	conn, err := ldap.DialURL(ldapURL, ldap.DialWithTLSConfig(&tls.Config{InsecureSkipVerify: insecureSkipVerify}))
	if err != nil {
		return nil, err
	}

	if startTLS && strings.HasPrefix(ldapURL, "ldap://") {
		// #nosec G402 -- skip TLS verification if configured
		if err := conn.StartTLS(&tls.Config{InsecureSkipVerify: insecureSkipVerify}); err != nil {
			_ = conn.Close()
			return nil, err
		}
	}

	return conn, nil
}
