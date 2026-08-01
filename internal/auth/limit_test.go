package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/define42/GitOne/internal/control"
)

type fixedControlStore struct {
	document control.Document
}

func (s fixedControlStore) Load(context.Context, string) (control.Document, error) {
	return s.document, nil
}

func (fixedControlStore) Invalidate(string) {}

type countedRejectingIdentityProvider struct {
	calls int
}

func (p *countedRejectingIdentityProvider) Authenticate(
	context.Context,
	string,
	string,
) (string, error) {
	p.calls++
	return "", errors.New("invalid credentials")
}

func TestAttemptLimiterAppliesUsernameAndIPBackoff(t *testing.T) {
	now := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	limiter := NewAttemptLimiter(AttemptLimiterOptions{
		MaximumConcurrent: 2,
		FailureThreshold:  2,
		InitialBackoff:    time.Second,
		MaximumBackoff:    time.Minute,
	})
	limiter.now = func() time.Time {
		return now
	}
	aliceFromFirstIP := WithClientIP(context.Background(), "192.0.2.10:1234")

	for range 2 {
		finish, err := limiter.Begin(aliceFromFirstIP, "Alice")
		if err != nil {
			t.Fatalf("initial attempt was limited: %v", err)
		}
		finish(false)
	}

	aliceFromSecondIP := WithClientIP(context.Background(), "192.0.2.11:1234")
	if _, err := limiter.Begin(aliceFromSecondIP, "alice"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("username backoff error = %v, want %v", err, ErrRateLimited)
	}
	bobFromFirstIP := WithClientIP(context.Background(), "192.0.2.10:5678")
	if _, err := limiter.Begin(bobFromFirstIP, "bob"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("IP backoff error = %v, want %v", err, ErrRateLimited)
	}

	now = now.Add(time.Second)
	finish, err := limiter.Begin(aliceFromFirstIP, "alice")
	if err != nil {
		t.Fatalf("attempt after backoff was limited: %v", err)
	}
	finish(true)
	finish, err = limiter.Begin(aliceFromFirstIP, "alice")
	if err != nil {
		t.Fatalf("successful authentication did not clear backoff: %v", err)
	}
	finish(true)
}

func TestAttemptLimiterBoundsConcurrentFirstAttempts(t *testing.T) {
	limiter := NewAttemptLimiter(AttemptLimiterOptions{
		MaximumConcurrent: 2,
		FailureThreshold:  5,
	})
	firstIP := WithClientIP(context.Background(), "192.0.2.20:1234")
	releases := make([]func(bool), 0, 2)
	for range 2 {
		finish, err := limiter.Begin(firstIP, "alice")
		if err != nil {
			t.Fatalf("initial concurrent attempt was limited: %v", err)
		}
		releases = append(releases, finish)
	}

	secondIP := WithClientIP(context.Background(), "192.0.2.21:1234")
	if _, err := limiter.Begin(secondIP, "ALICE"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("concurrent username limit error = %v, want %v", err, ErrRateLimited)
	}
	if _, err := limiter.Begin(firstIP, "bob"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("concurrent IP limit error = %v, want %v", err, ErrRateLimited)
	}

	for _, finish := range releases {
		finish(true)
	}
	finish, err := limiter.Begin(secondIP, "alice")
	if err != nil {
		t.Fatalf("released attempt slot remained limited: %v", err)
	}
	finish(true)
}

func TestSecretKDFLimiterRejectsExcessWork(t *testing.T) {
	hash, err := HashSecret("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	limiter := newSecretKDFLimiter(1)
	release, err := limiter.acquire()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = verifySecret(limiter, hash, "correct horse battery staple"); !errors.Is(
		err,
		ErrRateLimited,
	) {
		t.Fatalf("token KDF excess error = %v, want %v", err, ErrRateLimited)
	}
	release()

	matched, err := verifySecret(limiter, hash, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !matched {
		t.Fatal("secret did not verify after the token KDF slot was released")
	}
}

func TestResolverDoesNotBindLDAPWhenSecretKDFIsBusy(t *testing.T) {
	hash, err := HashSecret("actual-token-secret")
	if err != nil {
		t.Fatal(err)
	}
	limiter := newSecretKDFLimiter(1)
	release, err := limiter.acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	directory := &countedRejectingIdentityProvider{}
	resolver := Resolver{
		Controls: fixedControlStore{document: control.Document{
			Tokens: []control.Token{{
				Key:  "ci",
				Hash: hash,
				Role: control.RoleRead,
			}},
		}},
		Directory: directory,
		secretKDF: limiter,
	}

	_, err = resolver.Authenticate(
		context.Background(),
		"engineering",
		"ci",
		"wrong",
	)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("busy token KDF error = %v, want %v", err, ErrRateLimited)
	}
	if directory.calls != 0 {
		t.Fatalf("LDAP calls after busy token KDF = %d, want 0", directory.calls)
	}
}

func TestResolverKDFSaturationDoesNotCountAsAuthenticationFailure(t *testing.T) {
	hash, err := HashSecret("actual-token-secret")
	if err != nil {
		t.Fatal(err)
	}
	attempts := NewAttemptLimiter(AttemptLimiterOptions{
		MaximumConcurrent: 1,
		FailureThreshold:  5,
		InitialBackoff:    time.Minute,
		MaximumBackoff:    time.Minute,
	})
	now := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	attempts.now = func() time.Time { return now }
	kdf := newSecretKDFLimiter(1)
	release, err := kdf.acquire()
	if err != nil {
		t.Fatal(err)
	}
	resolver := Resolver{
		Controls: fixedControlStore{document: control.Document{
			Tokens: []control.Token{{
				Key:  "ci",
				Hash: hash,
				Role: control.RoleRead,
			}},
		}},
		Directory: &countedRejectingIdentityProvider{},
		Attempts:  attempts,
		secretKDF: kdf,
	}

	for _, remoteAddress := range []string{
		"192.0.2.31:1234",
		"192.0.2.32:1234",
		"192.0.2.33:1234",
		"192.0.2.34:1234",
		"192.0.2.35:1234",
	} {
		ctx := WithClientIP(context.Background(), remoteAddress)
		if _, authErr := resolver.Authenticate(
			ctx,
			"engineering",
			"ci",
			"actual-token-secret",
		); !errors.Is(authErr, ErrRateLimited) {
			t.Fatalf("busy KDF authentication error = %v, want %v", authErr, ErrRateLimited)
		}
	}
	finish, err := attempts.Begin(
		WithClientIP(context.Background(), "192.0.2.36:1234"),
		"ci",
	)
	if err != nil {
		t.Fatalf("KDF saturation created username backoff: %v", err)
	}
	finish(false)
	if _, authErr := resolver.Authenticate(
		WithClientIP(context.Background(), "192.0.2.37:1234"),
		"engineering",
		"ci",
		"actual-token-secret",
	); !errors.Is(authErr, ErrRateLimited) {
		t.Fatalf("busy KDF authentication error = %v, want %v", authErr, ErrRateLimited)
	}

	attempts.mutex.Lock()
	statePointer := attempts.states["user:ci"]
	if statePointer == nil {
		attempts.mutex.Unlock()
		t.Fatal("username attempt state was not recorded")
	}
	state := *statePointer
	attempts.mutex.Unlock()
	if state.failures != 1 || state.inFlight != 0 || !state.retryAt.IsZero() {
		t.Fatalf("KDF saturation changed attempt state: %#v", state)
	}

	release()
	principal, err := resolver.Authenticate(
		WithClientIP(context.Background(), "192.0.2.38:1234"),
		"engineering",
		"ci",
		"actual-token-secret",
	)
	if err != nil {
		t.Fatalf("correct token remained locked out after KDF slot release: %v", err)
	}
	if principal.Name != "ci" || principal.Role != control.RoleRead {
		t.Fatalf("authenticated principal = %#v", principal)
	}
}
