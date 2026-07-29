package auth

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestSessionManagerSignsAndEncryptsCookie(t *testing.T) {
	manager, err := NewSessionManager(SessionConfig{
		HashKey:  []byte(strings.Repeat("h", 64)),
		BlockKey: []byte(strings.Repeat("b", 32)),
		MaxAge:   time.Hour,
		Secure:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	header, err := manager.CookieHeader("alice")
	if err != nil {
		t.Fatal(err)
	}
	for _, attribute := range []string{
		SessionCookieName + "=",
		"Path=/",
		"Max-Age=3600",
		"HttpOnly",
		"Secure",
		"SameSite=Strict",
	} {
		if !strings.Contains(header, attribute) {
			t.Fatalf("session cookie is missing %q: %s", attribute, header)
		}
	}
	cookie := strings.SplitN(header, ";", 2)[0]
	if strings.Contains(cookie, "alice") {
		t.Fatal("session username was visible in the encrypted cookie")
	}
	username, err := manager.Username(cookie)
	if err != nil || username != "alice" {
		t.Fatalf("decode session cookie: username=%q err=%v", username, err)
	}
	replacement := "x"
	if strings.HasSuffix(cookie, replacement) {
		replacement = "y"
	}
	tampered := cookie[:len(cookie)-1] + replacement
	if _, err = manager.Username(tampered); err == nil {
		t.Fatal("tampered session cookie was accepted")
	}
	if _, err = manager.CookieHeader(" "); err == nil {
		t.Fatal("empty session username was accepted")
	}
	if _, err = manager.Username(""); err == nil {
		t.Fatal("missing session cookie was accepted")
	}
	if header := manager.ClearCookieHeader(); !strings.Contains(header, "Max-Age=0") {
		t.Fatalf("logout did not clear the session cookie: %s", header)
	}
}

func TestSessionConfigFromEnvironment(t *testing.T) {
	t.Setenv("GITONE_SESSION_HASH_KEY", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("h", 64))))
	t.Setenv("GITONE_SESSION_BLOCK_KEY", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("b", 32))))
	t.Setenv("GITONE_SESSION_MAX_AGE", "2h")
	t.Setenv("GITONE_SESSION_SECURE", "false")
	config, ephemeral, err := SessionConfigFromEnvironment(true)
	if err != nil {
		t.Fatal(err)
	}
	if ephemeral || config.MaxAge != 2*time.Hour || config.Secure ||
		len(config.HashKey) != 64 || len(config.BlockKey) != 32 {
		t.Fatalf("unexpected session config: %#v ephemeral=%v", config, ephemeral)
	}
}

func TestDecodeSessionKey(t *testing.T) {
	want := []byte("session key material")
	for _, test := range []struct {
		name, value string
		wantErr     bool
	}{
		{name: "standard Base64", value: base64.StdEncoding.EncodeToString(want)},
		{name: "raw Base64", value: base64.RawStdEncoding.EncodeToString(want)},
		{name: "invalid", value: "not base64!", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			key, err := decodeSessionKey(test.value)
			if test.wantErr {
				if err == nil {
					t.Fatalf("decodeSessionKey(%q) unexpectedly succeeded", test.value)
				}
				return
			}
			if err != nil || !bytes.Equal(key, want) {
				t.Fatalf("decodeSessionKey() = %q, %v; want %q", key, err, want)
			}
		})
	}
}

func TestSessionConfigurationRejectsInvalidValues(t *testing.T) {
	validHash := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("h", 64)))
	validBlock := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("b", 32)))
	for _, test := range []struct {
		name, hash, block, maxAge, secure string
	}{
		{name: "hash without block", hash: validHash},
		{name: "block without hash", block: validBlock},
		{name: "invalid hash encoding", hash: "not base64!", block: validBlock},
		{name: "invalid block encoding", hash: validHash, block: "not base64!"},
		{name: "invalid maximum age", hash: validHash, block: validBlock, maxAge: "tomorrow"},
		{name: "maximum age too short", hash: validHash, block: validBlock, maxAge: "500ms"},
		{name: "invalid secure flag", hash: validHash, block: validBlock, secure: "sometimes"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("GITONE_SESSION_HASH_KEY", test.hash)
			t.Setenv("GITONE_SESSION_BLOCK_KEY", test.block)
			t.Setenv("GITONE_SESSION_MAX_AGE", test.maxAge)
			t.Setenv("GITONE_SESSION_SECURE", test.secure)
			if _, _, err := SessionConfigFromEnvironment(true); err == nil {
				t.Fatal("invalid session configuration was accepted")
			}
		})
	}

	for _, test := range []struct {
		name   string
		config SessionConfig
	}{
		{
			name: "short hash key",
			config: SessionConfig{
				HashKey:  []byte(strings.Repeat("h", 31)),
				BlockKey: []byte(strings.Repeat("b", 32)),
			},
		},
		{
			name: "invalid block key",
			config: SessionConfig{
				HashKey:  []byte(strings.Repeat("h", 64)),
				BlockKey: []byte(strings.Repeat("b", 20)),
			},
		},
		{
			name: "maximum age too short",
			config: SessionConfig{
				HashKey:  []byte(strings.Repeat("h", 64)),
				BlockKey: []byte(strings.Repeat("b", 32)),
				MaxAge:   time.Millisecond,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewSessionManager(test.config); err == nil {
				t.Fatal("invalid session manager configuration was accepted")
			}
		})
	}
}

func TestSessionManagerUsesDefaultMaximumAge(t *testing.T) {
	manager, err := NewSessionManager(SessionConfig{
		HashKey:  []byte(strings.Repeat("h", 64)),
		BlockKey: []byte(strings.Repeat("b", 16)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if manager.maxAge != defaultSessionDuration {
		t.Fatalf("default maximum age = %s, want %s", manager.maxAge, defaultSessionDuration)
	}
}
