package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/define42/GitOne/internal/control"
	"golang.org/x/crypto/argon2"
)

const (
	argonMemory       = 19 * 1024
	argonIterations   = 2
	argonParallelism  = 1
	argonSaltBytes    = 16
	argonKeyBytes     = 32
	tokenSecretBytes  = 24
	TokenSecretLength = 32
)

type Resolver struct {
	Controls  ControlStore
	Directory IdentityProvider
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
	type groupControl struct {
		path     string
		document control.Document
	}
	documents := make([]groupControl, 0)
	paths := parents(group)
	for i := len(paths) - 1; i >= 0; i-- {
		document, err := r.Controls.Load(ctx, paths[i])
		if err != nil {
			return Principal{}, fmt.Errorf("load group control %q: %w", paths[i], err)
		}
		documents = append(documents, groupControl{path: paths[i], document: document})
		for _, token := range document.Tokens {
			if token.Disabled || token.Key != user ||
				(token.ExpiresAt != nil && time.Now().After(*token.ExpiresAt)) {
				continue
			}
			if VerifySecret(token.Hash, secret) {
				return Principal{
					Name:  user,
					Role:  token.Role,
					Group: paths[i],
				}, nil
			}
		}
		if !document.Inherit {
			break
		}
	}
	identity, err := r.AuthenticateIdentity(ctx, user, secret)
	if err != nil {
		return Principal{}, errors.New("invalid credentials")
	}
	for _, candidate := range documents {
		if role, ok := candidate.document.Members[identity.Name]; ok {
			identity.Role = role
			identity.Group = candidate.path
			return identity, nil
		}
	}
	return Principal{}, errors.New("invalid credentials")
}

func (r *Resolver) AuthenticateIdentity(
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

func (r *Resolver) AuthorizeIdentity(
	ctx context.Context,
	group string,
	identity Principal,
) (Principal, error) {
	paths := parents(group)
	for i := len(paths) - 1; i >= 0; i-- {
		document, err := r.Controls.Load(ctx, paths[i])
		if err != nil {
			return Principal{}, fmt.Errorf("load group control %q: %w", paths[i], err)
		}
		if role, ok := document.Members[identity.Name]; ok {
			identity.Role = role
			identity.Group = paths[i]
			return identity, nil
		}
		if !document.Inherit {
			break
		}
	}
	return Principal{}, errors.New("identity is not authorized")
}

func parents(g string) []string {
	p := strings.Split(g, "/")
	o := make([]string, len(p))
	for i := range p {
		o[i] = strings.Join(p[:i+1], "/")
	}
	return o
}

func HashSecret(secret string) (string, error) {
	if secret == "" {
		return "", errors.New("secret is required")
	}
	salt := make([]byte, argonSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	hash := argon2.IDKey([]byte(secret), salt, argonIterations, argonMemory, argonParallelism, argonKeyBytes)
	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		argonMemory,
		argonIterations,
		argonParallelism,
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
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	version, ok := parseValue(parts[2], "v")
	if !ok || version != argon2.Version {
		return false
	}
	var memory, iterations uint64
	var parallelism uint64
	parameters := strings.Split(parts[3], ",")
	if len(parameters) != 3 {
		return false
	}
	if memory, ok = parseValue(parameters[0], "m"); !ok || memory < 8*1024 || memory > 256*1024 {
		return false
	}
	if iterations, ok = parseValue(parameters[1], "t"); !ok || iterations < 1 || iterations > 10 {
		return false
	}
	if parallelism, ok = parseValue(parameters[2], "p"); !ok || parallelism < 1 || parallelism > 16 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 16 || len(salt) > 64 {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(expected) < 16 || len(expected) > 64 {
		return false
	}
	actual := argon2.IDKey(
		[]byte(secret),
		salt,
		uint32(iterations),
		uint32(memory),
		uint8(parallelism),
		uint32(len(expected)),
	)
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

func parseValue(value, key string) (uint64, bool) {
	prefix := key + "="
	if !strings.HasPrefix(value, prefix) {
		return 0, false
	}
	parsed, err := strconv.ParseUint(strings.TrimPrefix(value, prefix), 10, 32)
	return parsed, err == nil
}
