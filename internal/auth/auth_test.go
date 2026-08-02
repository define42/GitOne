package auth_test

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/define42/GitOne/internal/auth"
	"github.com/define42/GitOne/internal/control"
	"github.com/define42/GitOne/internal/storage"
)

type fakeDirectory struct {
	username  string
	password  string
	canonical string
}

type controllableControlStore struct {
	store       *control.Store
	unavailable map[string]error
}

func (s *controllableControlStore) Load(
	ctx context.Context,
	group string,
) (control.Document, error) {
	if err := s.unavailable[group]; err != nil {
		return control.Document{}, err
	}
	return s.store.Load(ctx, group)
}

func (s *controllableControlStore) Invalidate(group string) {
	s.store.Invalidate(group)
}

func (d fakeDirectory) Authenticate(
	_ context.Context,
	username string,
	password string,
) (string, error) {
	if username != d.username || password != d.password {
		return "", errors.New("invalid credentials")
	}
	return d.canonical, nil
}

func TestHashSecretUsesFIPSApprovedPBKDF2(t *testing.T) {
	if _, err := auth.HashSecret(""); err == nil {
		t.Fatal("empty secret was hashed")
	}
	hash, err := auth.HashSecret("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$pbkdf2-sha256$") {
		t.Fatalf("unexpected hash format: %q", hash)
	}
	if !auth.VerifySecret(hash, "correct horse battery staple") {
		t.Fatal("generated hash did not verify")
	}
	if auth.VerifySecret(hash, "wrong") {
		t.Fatal("wrong secret verified")
	}
	if auth.VerifySecret("sha256:deadbeef", "anything") {
		t.Fatal("legacy unsalted SHA-256 hash verified")
	}
	parts := strings.Split(hash, "$")
	malformed := []string{
		"$argon2id$v=19$m=19456,t=2,p=1$c2FsdA$aGFzaA",
		strings.Join([]string{"", "pbkdf2-sha512", parts[2], parts[3], parts[4]}, "$"),
		strings.Join([]string{"", "pbkdf2-sha256", "i=99999", parts[3], parts[4]}, "$"),
		strings.Join([]string{"", "pbkdf2-sha256", "i=2000001", parts[3], parts[4]}, "$"),
		strings.Join([]string{"", "pbkdf2-sha256", "i=bad", parts[3], parts[4]}, "$"),
		strings.Join([]string{"", "pbkdf2-sha256", parts[2], "bad!", parts[4]}, "$"),
		strings.Join([]string{
			"",
			"pbkdf2-sha256",
			parts[2],
			base64.RawStdEncoding.EncodeToString(make([]byte, 8)),
			parts[4],
		}, "$"),
		strings.Join([]string{"", "pbkdf2-sha256", parts[2], parts[3], "bad!"}, "$"),
		strings.Join([]string{
			"",
			"pbkdf2-sha256",
			parts[2],
			parts[3],
			base64.RawStdEncoding.EncodeToString(make([]byte, 16)),
		}, "$"),
	}
	for _, encoded := range malformed {
		if auth.VerifySecret(encoded, "correct horse battery staple") {
			t.Fatalf("malformed hash verified: %q", encoded)
		}
	}
}

func TestGenerateTokenSecretReturns32RandomURLSafeCharacters(t *testing.T) {
	first, err := auth.GenerateTokenSecret()
	if err != nil {
		t.Fatal(err)
	}
	second, err := auth.GenerateTokenSecret()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != auth.TokenSecretLength || len(second) != auth.TokenSecretLength {
		t.Fatalf("generated secret lengths = %d and %d", len(first), len(second))
	}
	if first == second {
		t.Fatal("two generated token secrets were identical")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(first)
	if err != nil {
		t.Fatalf("generated secret is not unpadded base64url: %v", err)
	}
	if len(decoded) != 24 {
		t.Fatalf("generated secret contains %d random bytes, want 24", len(decoded))
	}
	hash, err := auth.HashSecret(first)
	if err != nil {
		t.Fatal(err)
	}
	if !auth.VerifySecret(hash, first) {
		t.Fatal("generated token secret did not verify against its hash")
	}
}

func TestResolverUsesRootMemberAndTokenRolesForDeepGroups(t *testing.T) {
	root := t.TempDir()
	store := storage.Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	controls := control.NewStore(root)
	document, err := controls.Load(context.Background(), "engineering")
	if err != nil {
		t.Fatal(err)
	}
	document.Inherit = false
	tokenHash, err := auth.HashSecret("ci-secret")
	if err != nil {
		t.Fatal(err)
	}
	document.Members["bob"] = control.RoleRead
	document.Tokens = []control.Token{{
		Name: "deploy",
		Key:  "ci",
		Hash: tokenHash,
		Role: control.RoleDeveloper,
	}}
	if err = store.UpdateGroupControl("engineering", document, "alice"); err != nil {
		t.Fatal(err)
	}
	controls.Invalidate("engineering")
	resolver := auth.Resolver{
		Controls: controls,
		Directory: fakeDirectory{
			username:  "bob-login",
			password:  "bob-secret",
			canonical: "bob",
		},
	}
	deepGroup := "engineering/backend/platform"

	if _, err = resolver.Authenticate(context.Background(), deepGroup, "bob-login", "wrong"); err == nil {
		t.Fatal("member authenticated with an invalid LDAP password")
	}
	principal, err := resolver.Authenticate(
		context.Background(),
		deepGroup,
		"bob-login",
		"bob-secret",
	)
	if err != nil {
		t.Fatalf("LDAP member did not authenticate: %v", err)
	}
	if principal.Name != "bob" || principal.Role != control.RoleRead ||
		principal.Group != "engineering" {
		t.Fatalf("canonical LDAP username was not authorized: %#v", principal)
	}
	principal, err = resolver.AuthorizeIdentity(
		context.Background(),
		deepGroup,
		auth.Principal{Name: "bob"},
	)
	if err != nil {
		t.Fatalf("deep session identity was not authorized: %v", err)
	}
	if principal.Role != control.RoleRead || principal.Group != "engineering" {
		t.Fatalf("deep session identity did not receive the root role: %#v", principal)
	}
	if _, err = resolver.Authenticate(context.Background(), deepGroup, "deploy", "ci-secret"); err == nil {
		t.Fatal("token display name was accepted as its login key")
	}
	principal, err = resolver.Authenticate(context.Background(), deepGroup, "ci", "ci-secret")
	if err != nil {
		t.Fatalf("root token key did not authenticate for a deep group: %v", err)
	}
	if principal.Group != "engineering" || principal.Role != control.RoleDeveloper {
		t.Fatalf("token did not receive its root group role: %#v", principal)
	}
}

func TestResolverFailsClosedWhenRootControlCannotBeLoaded(t *testing.T) {
	for _, test := range []struct {
		name    string
		disrupt func(*testing.T, string)
	}{
		{
			name: "missing",
			disrupt: func(t *testing.T, rootControlPath string) {
				t.Helper()
				if err := os.RemoveAll(rootControlPath); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "corrupt",
			disrupt: func(t *testing.T, rootControlPath string) {
				t.Helper()
				referencePath := filepath.Join(rootControlPath, "refs", "heads", "main")
				if err := os.WriteFile(referencePath, []byte("not-a-git-hash\n"), 0o640); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, controls := rootControlFixture(t)
			test.disrupt(t, filepath.Join(root, "engineering", "control.git"))
			controls.Invalidate("engineering/backend")

			resolver := auth.Resolver{
				Controls: controls,
				Directory: fakeDirectory{
					username:  "alice-login",
					password:  "alice-secret",
					canonical: "alice",
				},
			}
			assertRootControlLoadDenied(t, &resolver)
		})
	}
}

func TestResolverFailsClosedDuringTemporaryRootControlFailure(t *testing.T) {
	_, controls := rootControlFixture(t)
	temporaryErr := errors.New("control storage temporarily unavailable")
	controlled := &controllableControlStore{
		store: controls,
		unavailable: map[string]error{
			"engineering": temporaryErr,
		},
	}
	resolver := auth.Resolver{
		Controls: controlled,
		Directory: fakeDirectory{
			username:  "alice-login",
			password:  "alice-secret",
			canonical: "alice",
		},
	}

	assertRootControlLoadDenied(t, &resolver)

	delete(controlled.unavailable, "engineering")
	principal, err := resolver.Authenticate(
		context.Background(),
		"engineering/backend/platform",
		"alice-login",
		"alice-secret",
	)
	if err != nil {
		t.Fatalf("authentication did not recover with the control store: %v", err)
	}
	if principal.Name != "alice" || principal.Role != control.RoleOwner ||
		principal.Group != "engineering" {
		t.Fatalf("unexpected recovered principal: %#v", principal)
	}
	principal, err = resolver.AuthorizeIdentity(
		context.Background(),
		"engineering/backend/platform",
		auth.Principal{Name: "alice"},
	)
	if err != nil {
		t.Fatalf("session authorization did not recover with the control store: %v", err)
	}
	if principal.Role != control.RoleOwner || principal.Group != "engineering" {
		t.Fatalf("unexpected recovered session principal: %#v", principal)
	}
}

func rootControlFixture(t *testing.T) (string, *control.Store) {
	t.Helper()
	root := t.TempDir()
	store := storage.Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	controls := control.NewStore(root)
	return root, controls
}

func assertRootControlLoadDenied(t *testing.T, resolver *auth.Resolver) {
	t.Helper()
	if _, err := resolver.Authenticate(
		context.Background(),
		"engineering/backend/platform",
		"alice-login",
		"alice-secret",
	); err == nil {
		t.Fatal("LDAP authentication succeeded after the root control load failed")
	} else if !strings.Contains(err.Error(), `load root group control "engineering"`) {
		t.Fatalf("LDAP authentication returned an unexpected error: %v", err)
	}
	if _, err := resolver.AuthorizeIdentity(
		context.Background(),
		"engineering/backend/platform",
		auth.Principal{Name: "alice"},
	); err == nil {
		t.Fatal("session authorization succeeded after the root control load failed")
	} else if !strings.Contains(err.Error(), `load root group control "engineering"`) {
		t.Fatalf("session authorization returned an unexpected error: %v", err)
	}
}
