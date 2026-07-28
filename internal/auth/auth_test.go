package auth_test

import (
	"context"
	"errors"
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
		Name:         "deploy",
		Key:          "ci",
		Hash:         tokenHash,
		Role:         control.RoleWrite,
		Repositories: []string{"api"},
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
	if !principal.AllowsRepository("api") || principal.AllowsRepository("web") {
		t.Fatalf("repository scope was not retained: %#v", principal.Repositories)
	}
}
