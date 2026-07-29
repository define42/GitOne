package auth_test

import (
	"context"
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

func TestHashSecretUsesArgon2id(t *testing.T) {
	if _, err := auth.HashSecret(""); err == nil {
		t.Fatal("empty secret was hashed")
	}
	hash, err := auth.HashSecret("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
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
		strings.Join([]string{"", "argon2id", "v=16", parts[3], parts[4], parts[5]}, "$"),
		strings.Join([]string{"", "argon2id", parts[2], "m=65536,t=3", parts[4], parts[5]}, "$"),
		strings.Join([]string{"", "argon2id", parts[2], "m=1,t=3,p=2", parts[4], parts[5]}, "$"),
		strings.Join([]string{"", "argon2id", parts[2], "m=65536,t=0,p=2", parts[4], parts[5]}, "$"),
		strings.Join([]string{"", "argon2id", parts[2], "m=65536,t=3,p=0", parts[4], parts[5]}, "$"),
		strings.Join([]string{"", "argon2id", parts[2], parts[3], "bad!", parts[5]}, "$"),
		strings.Join([]string{"", "argon2id", parts[2], parts[3], parts[4], "bad!"}, "$"),
	}
	for _, encoded := range malformed {
		if auth.VerifySecret(encoded, "correct horse battery staple") {
			t.Fatalf("malformed hash verified: %q", encoded)
		}
	}
}

func TestResolverUsesLDAPIdentityAndTokenKeys(t *testing.T) {
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

	if _, err = resolver.Authenticate(context.Background(), "engineering", "bob-login", "wrong"); err == nil {
		t.Fatal("member authenticated with an invalid LDAP password")
	}
	principal, err := resolver.Authenticate(
		context.Background(),
		"engineering",
		"bob-login",
		"bob-secret",
	)
	if err != nil {
		t.Fatalf("LDAP member did not authenticate: %v", err)
	}
	if principal.Name != "bob" || principal.Role != control.RoleRead {
		t.Fatalf("canonical LDAP username was not authorized: %#v", principal)
	}
	if _, err = resolver.Authenticate(context.Background(), "engineering", "deploy", "ci-secret"); err == nil {
		t.Fatal("token display name was accepted as its login key")
	}
	principal, err = resolver.Authenticate(context.Background(), "engineering", "ci", "ci-secret")
	if err != nil {
		t.Fatalf("token key did not authenticate: %v", err)
	}
	if principal.Group != "engineering" || principal.Role != control.RoleDeveloper {
		t.Fatalf("token did not receive its group role: %#v", principal)
	}
}

func TestResolverFailsClosedWhenChildControlCannotBeLoaded(t *testing.T) {
	for _, test := range []struct {
		name    string
		disrupt func(*testing.T, string)
	}{
		{
			name: "missing",
			disrupt: func(t *testing.T, childControlPath string) {
				t.Helper()
				if err := os.RemoveAll(childControlPath); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "corrupt",
			disrupt: func(t *testing.T, childControlPath string) {
				t.Helper()
				referencePath := filepath.Join(childControlPath, "refs", "heads", "main")
				if err := os.WriteFile(referencePath, []byte("not-a-git-hash\n"), 0o640); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, controls := inheritedControlFixture(t, false)
			test.disrupt(t, filepath.Join(root, "engineering", "backend", "control.git"))
			controls.Invalidate("engineering/backend")

			resolver := auth.Resolver{
				Controls: controls,
				Directory: fakeDirectory{
					username:  "alice-login",
					password:  "alice-secret",
					canonical: "alice",
				},
			}
			assertControlLoadDenied(t, &resolver)
		})
	}
}

func TestResolverFailsClosedDuringTemporaryChildControlFailure(t *testing.T) {
	_, controls := inheritedControlFixture(t, true)
	temporaryErr := errors.New("control storage temporarily unavailable")
	controlled := &controllableControlStore{
		store: controls,
		unavailable: map[string]error{
			"engineering/backend": temporaryErr,
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

	assertControlLoadDenied(t, &resolver)

	delete(controlled.unavailable, "engineering/backend")
	principal, err := resolver.Authenticate(
		context.Background(),
		"engineering/backend",
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
		"engineering/backend",
		auth.Principal{Name: "alice"},
	)
	if err != nil {
		t.Fatalf("session authorization did not recover with the control store: %v", err)
	}
	if principal.Role != control.RoleOwner || principal.Group != "engineering" {
		t.Fatalf("unexpected recovered session principal: %#v", principal)
	}
}

func inheritedControlFixture(t *testing.T, inherit bool) (string, *control.Store) {
	t.Helper()
	root := t.TempDir()
	store := storage.Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateGroup("engineering/backend", "bob", ""); err != nil {
		t.Fatal(err)
	}
	controls := control.NewStore(root)
	document, err := controls.Load(context.Background(), "engineering/backend")
	if err != nil {
		t.Fatal(err)
	}
	document.Inherit = inherit
	if err = store.UpdateGroupControl("engineering/backend", document, "bob"); err != nil {
		t.Fatal(err)
	}
	controls.Invalidate("engineering/backend")
	return root, controls
}

func assertControlLoadDenied(t *testing.T, resolver *auth.Resolver) {
	t.Helper()
	if _, err := resolver.Authenticate(
		context.Background(),
		"engineering/backend",
		"alice-login",
		"alice-secret",
	); err == nil {
		t.Fatal("LDAP authentication accepted parent access after the child control load failed")
	} else if !strings.Contains(err.Error(), `load group control "engineering/backend"`) {
		t.Fatalf("LDAP authentication returned an unexpected error: %v", err)
	}
	if _, err := resolver.AuthorizeIdentity(
		context.Background(),
		"engineering/backend",
		auth.Principal{Name: "alice"},
	); err == nil {
		t.Fatal("session authorization accepted parent access after the child control load failed")
	} else if !strings.Contains(err.Error(), `load group control "engineering/backend"`) {
		t.Fatalf("session authorization returned an unexpected error: %v", err)
	}
}
