package auth

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/define42/GitOne/internal/control"
	"github.com/define42/GitOne/internal/repopath"
)

const (
	pbkdf2Iterations        = 100_000
	minimumPBKDF2Iterations = 100_000
	maximumPBKDF2Iterations = 2_000_000
	pbkdf2SaltBytes         = 16
	pbkdf2KeyBytes          = 32
	tokenSecretBytes        = 24
	TokenSecretLength       = 32
)

type Resolver struct {
	Controls  ControlStore
	Directory IdentityProvider
	Attempts  *AttemptLimiter
	secretKDF *secretKDFLimiter
}

type ControlStore interface {
	Load(context.Context, string) (control.Document, error)
	Invalidate(string)
}

type IdentityProvider interface {
	Authenticate(context.Context, string, string) (string, error)
}

type Principal struct {
	Name  string
	Role  control.Role
	Group string
}

func (r *Resolver) Authenticate(ctx context.Context, group, user, secret string) (Principal, error) {
	finish, err := r.beginAttempt(ctx, user)
	if err != nil {
		return Principal{}, err
	}
	outcome := attemptFailed
	defer func() {
		finish(outcome)
	}()

	rootGroup, err := repopath.RootGroup(group)
	if err != nil {
		return Principal{}, err
	}
	document, err := r.Controls.Load(ctx, rootGroup)
	if err != nil {
		return Principal{}, fmt.Errorf("load root group control %q: %w", rootGroup, err)
	}
	for _, token := range document.Tokens {
		if token.Disabled || token.Key != user ||
			(token.ExpiresAt != nil && time.Now().After(*token.ExpiresAt)) {
			continue
		}
		matched, verifyErr := verifySecret(r.secretKDFLimiter(), token.Hash, secret)
		if verifyErr != nil {
			// KDF admission or execution failed before a credential verdict.
			// Release the attempt reservation without counting a failure or
			// clearing any prior failures.
			outcome = attemptAborted
			return Principal{}, verifyErr
		}
		if matched {
			outcome = attemptSucceeded
			return Principal{
				Name:  user,
				Role:  token.Role,
				Group: rootGroup,
			}, nil
		}
	}
	identity, err := r.authenticateIdentity(ctx, user, secret)
	if err != nil {
		return Principal{}, errors.New("invalid credentials")
	}
	if role, ok := document.Members[identity.Name]; ok {
		identity.Role = role
		identity.Group = rootGroup
		outcome = attemptSucceeded
		return identity, nil
	}
	return Principal{}, errors.New("invalid credentials")
}

func (r *Resolver) AuthenticateIdentity(
	ctx context.Context,
	user string,
	secret string,
) (Principal, error) {
	finish, err := r.beginAttempt(ctx, user)
	if err != nil {
		return Principal{}, err
	}
	outcome := attemptFailed
	defer func() {
		finish(outcome)
	}()
	principal, err := r.authenticateIdentity(ctx, user, secret)
	if err == nil {
		outcome = attemptSucceeded
	}
	return principal, err
}

func (r *Resolver) authenticateIdentity(
	ctx context.Context,
	user string,
	secret string,
) (Principal, error) {
	if r.Directory == nil {
		return Principal{}, errors.New("directory authentication is not configured")
	}
	canonical, err := r.Directory.Authenticate(ctx, user, secret)
	if err != nil || canonical == "" {
		return Principal{}, errors.New("invalid credentials")
	}
	return Principal{Name: canonical}, nil
}

func (r *Resolver) beginAttempt(
	ctx context.Context,
	user string,
) (func(attemptOutcome), error) {
	if r.Attempts == nil {
		return func(attemptOutcome) {}, nil
	}
	return r.Attempts.begin(ctx, user)
}

func (r *Resolver) secretKDFLimiter() *secretKDFLimiter {
	if r.secretKDF != nil {
		return r.secretKDF
	}
	return processSecretKDFLimiter
}

func (r *Resolver) AuthorizeIdentity(
	ctx context.Context,
	group string,
	identity Principal,
) (Principal, error) {
	rootGroup, err := repopath.RootGroup(group)
	if err != nil {
		return Principal{}, err
	}
	document, err := r.Controls.Load(ctx, rootGroup)
	if err != nil {
		return Principal{}, fmt.Errorf("load root group control %q: %w", rootGroup, err)
	}
	if role, ok := document.Members[identity.Name]; ok {
		identity.Role = role
		identity.Group = rootGroup
		return identity, nil
	}
	return Principal{}, errors.New("identity is not authorized")
}

func HashSecret(secret string) (string, error) {
	if secret == "" {
		return "", errors.New("secret is required")
	}
	salt := make([]byte, pbkdf2SaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	release, err := processSecretKDFLimiter.acquire()
	if err != nil {
		return "", err
	}
	defer release()
	hash, err := pbkdf2.Key(sha256.New, secret, salt, pbkdf2Iterations, pbkdf2KeyBytes)
	if err != nil {
		return "", fmt.Errorf("derive token secret hash: %w", err)
	}
	return fmt.Sprintf(
		"$pbkdf2-sha256$i=%d$%s$%s",
		pbkdf2Iterations,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

// GenerateTokenSecret returns a cryptographically random, URL-safe token
// secret. Twenty-four random bytes encode to exactly 32 base64url characters.
func GenerateTokenSecret() (string, error) {
	random := make([]byte, tokenSecretBytes)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate token secret: %w", err)
	}
	secret := base64.RawURLEncoding.EncodeToString(random)
	if len(secret) != TokenSecretLength {
		return "", fmt.Errorf("generated token secret has unexpected length %d", len(secret))
	}
	return secret, nil
}

func VerifySecret(encoded, secret string) bool {
	matched, err := verifySecret(processSecretKDFLimiter, encoded, secret)
	return err == nil && matched
}

func verifySecret(limiter *secretKDFLimiter, encoded, secret string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 5 || parts[1] != "pbkdf2-sha256" {
		return false, nil
	}
	iterations, ok := parseValue(parts[2], "i")
	if !ok || iterations < minimumPBKDF2Iterations || iterations > maximumPBKDF2Iterations {
		return false, nil
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || len(salt) < 16 || len(salt) > 64 {
		return false, nil
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(expected) != pbkdf2KeyBytes {
		return false, nil
	}
	release, err := limiter.acquire()
	if err != nil {
		return false, err
	}
	defer release()
	actual, err := pbkdf2.Key(sha256.New, secret, salt, int(iterations), len(expected))
	if err != nil {
		return false, fmt.Errorf("derive token secret hash: %w", err)
	}
	return subtle.ConstantTimeCompare(actual, expected) == 1, nil
}

func parseValue(value, key string) (uint64, bool) {
	prefix := key + "="
	if !strings.HasPrefix(value, prefix) {
		return 0, false
	}
	parsed, err := strconv.ParseUint(strings.TrimPrefix(value, prefix), 10, 32)
	return parsed, err == nil
}
