package auth

import (
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
	tampered := cookie[:len(cookie)-1] + "x"
	if _, err = manager.Username(tampered); err == nil {
		t.Fatal("tampered session cookie was accepted")
	}
	if clear := manager.ClearCookieHeader(); !strings.Contains(clear, "Max-Age=0") {
		t.Fatalf("logout did not clear the session cookie: %s", clear)
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
