package auth

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/securecookie"
)

const (
	SessionCookieName      = "gitone_session"
	defaultSessionDuration = 12 * time.Hour
)

type SessionConfig struct {
	HashKey  []byte
	BlockKey []byte
	MaxAge   time.Duration
	Secure   bool
}

type SessionManager struct {
	codec  *securecookie.SecureCookie
	maxAge time.Duration
	secure bool
}

type sessionValue struct {
	Username string `json:"username"`
}

func SessionConfigFromEnvironment(secureDefault bool) (SessionConfig, bool, error) {
	config := SessionConfig{
		MaxAge: defaultSessionDuration,
		Secure: secureDefault,
	}
	hashValue := strings.TrimSpace(os.Getenv("GITONE_SESSION_HASH_KEY"))
	blockValue := strings.TrimSpace(os.Getenv("GITONE_SESSION_BLOCK_KEY"))
	ephemeral := hashValue == "" && blockValue == ""
	if ephemeral {
		config.HashKey = securecookie.GenerateRandomKey(64)
		config.BlockKey = securecookie.GenerateRandomKey(32)
		if config.HashKey == nil || config.BlockKey == nil {
			return config, true, errors.New("could not generate secure cookie keys")
		}
	} else {
		if hashValue == "" || blockValue == "" {
			return config, false, errors.New(
				"GITONE_SESSION_HASH_KEY and GITONE_SESSION_BLOCK_KEY must be configured together",
			)
		}
		var err error
		config.HashKey, err = decodeSessionKey(hashValue)
		if err != nil {
			return config, false, fmt.Errorf("GITONE_SESSION_HASH_KEY: %w", err)
		}
		config.BlockKey, err = decodeSessionKey(blockValue)
		if err != nil {
			return config, false, fmt.Errorf("GITONE_SESSION_BLOCK_KEY: %w", err)
		}
	}
	if value := strings.TrimSpace(os.Getenv("GITONE_SESSION_MAX_AGE")); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed < time.Second {
			return config, ephemeral, errors.New("GITONE_SESSION_MAX_AGE must be at least one second")
		}
		config.MaxAge = parsed
	}
	if value := strings.TrimSpace(os.Getenv("GITONE_SESSION_SECURE")); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return config, ephemeral, fmt.Errorf("GITONE_SESSION_SECURE: %w", err)
		}
		config.Secure = parsed
	}
	return config, ephemeral, nil
}

func NewSessionManager(config SessionConfig) (*SessionManager, error) {
	if len(config.HashKey) < 32 {
		return nil, errors.New("session hash key must be at least 32 bytes")
	}
	switch len(config.BlockKey) {
	case 16, 24, 32:
	default:
		return nil, errors.New("session block key must be 16, 24, or 32 bytes")
	}
	if config.MaxAge == 0 {
		config.MaxAge = defaultSessionDuration
	} else if config.MaxAge < time.Second {
		return nil, errors.New("session maximum age must be at least one second")
	}
	codec := securecookie.New(config.HashKey, config.BlockKey)
	codec.MaxAge(int(config.MaxAge.Seconds()))
	codec.SetSerializer(securecookie.JSONEncoder{})
	return &SessionManager{
		codec:  codec,
		maxAge: config.MaxAge,
		secure: config.Secure,
	}, nil
}

func NewEphemeralSessionManager(secure bool) (*SessionManager, error) {
	hashKey := securecookie.GenerateRandomKey(64)
	blockKey := securecookie.GenerateRandomKey(32)
	if hashKey == nil || blockKey == nil {
		return nil, errors.New("could not generate secure cookie keys")
	}
	return NewSessionManager(SessionConfig{
		HashKey:  hashKey,
		BlockKey: blockKey,
		MaxAge:   defaultSessionDuration,
		Secure:   secure,
	})
}

func (m *SessionManager) CookieHeader(username string) (string, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return "", errors.New("session username is required")
	}
	value, err := m.codec.Encode(SessionCookieName, sessionValue{Username: username})
	if err != nil {
		return "", fmt.Errorf("encode session cookie: %w", err)
	}
	return (&http.Cookie{
		Name:     SessionCookieName,
		Value:    value,
		Path:     "/",
		Expires:  time.Now().Add(m.maxAge),
		MaxAge:   int(m.maxAge.Seconds()),
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteStrictMode,
	}).String(), nil
}

func (m *SessionManager) Username(cookieHeader string) (string, error) {
	request := &http.Request{Header: http.Header{"Cookie": []string{cookieHeader}}}
	cookie, err := request.Cookie(SessionCookieName)
	if err != nil {
		return "", errors.New("session cookie is missing")
	}
	value := sessionValue{}
	if err = m.codec.Decode(SessionCookieName, cookie.Value, &value); err != nil {
		return "", errors.New("session cookie is invalid")
	}
	value.Username = strings.TrimSpace(value.Username)
	if value.Username == "" {
		return "", errors.New("session username is missing")
	}
	return value.Username, nil
}

func (m *SessionManager) ClearCookieHeader() string {
	return (&http.Cookie{
		Name:     SessionCookieName,
		Path:     "/",
		Expires:  time.Unix(1, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteStrictMode,
	}).String()
}

func decodeSessionKey(value string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(value)
	if err == nil {
		return key, nil
	}
	key, rawErr := base64.RawStdEncoding.DecodeString(value)
	if rawErr != nil {
		return nil, errors.New("must be base64 encoded")
	}
	return key, nil
}
