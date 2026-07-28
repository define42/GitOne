package httpapi

import (
	"context"
	"strings"
	"testing"

	"github.com/define42/GitOne/internal/auth"
	"github.com/define42/GitOne/internal/control"
	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/storage"
)

func TestRenameGroupControlsRejectsMissingAndExistingDestination(t *testing.T) {
	root := t.TempDir()
	store := storage.Store{Root: root}
	controls := control.NewStore(root)
	api := API{
		Storage:  store,
		Resolver: &auth.Resolver{Controls: controls},
	}

	err := api.renameGroupControls(
		context.Background(),
		"missing",
		"target",
		control.Document{},
		"alice",
	)
	if err == nil || !strings.Contains(err.Error(), "group not found") {
		t.Fatalf("missing group error = %v", err)
	}

	if err = store.CreateGroup("source", "alice", ""); err != nil {
		t.Fatal(err)
	}
	if err = store.CreateGroup("destination", "alice", ""); err != nil {
		t.Fatal(err)
	}
	current, err := controls.Load(context.Background(), "source")
	if err != nil {
		t.Fatal(err)
	}
	current.Group = "destination"
	err = api.renameGroupControls(
		context.Background(),
		"source",
		"destination",
		current,
		"alice",
	)
	if err == nil || !strings.Contains(err.Error(), "destination group exists") {
		t.Fatalf("existing destination error = %v", err)
	}
	unchanged, err := controls.Load(context.Background(), "source")
	if err != nil {
		t.Fatalf("source group moved after failed rename: %v", err)
	}
	if unchanged.Group != "source" {
		t.Fatalf("source control changed after failed rename: %#v", unchanged)
	}
}

func TestUpdateGroupSettingsRotatesAndPreservesTokenSecrets(t *testing.T) {
	service, credentials, _ := repositoryAPIFixture(t)
	ctx := context.Background()

	for _, name := range []string{"", "nested/group"} {
		_, err := service.updateGroupSettings(ctx, &updateGroupSettingsInput{
			GroupPathInput: GroupPathInput{AuthInput: credentials, Path: "engineering"},
			Body: updateGroupSettingsBody{
				Name:    name,
				Members: map[string]control.Role{"alice": control.RoleOwner},
			},
		})
		if err == nil {
			t.Fatalf("invalid group name %q was accepted", name)
		}
	}

	_, err := service.updateGroupSettings(ctx, &updateGroupSettingsInput{
		GroupPathInput: GroupPathInput{AuthInput: credentials, Path: "engineering"},
		Body: updateGroupSettingsBody{
			Name:    "engineering",
			Members: map[string]control.Role{"alice": control.RoleOwner},
			Tokens: []groupTokenInput{{
				Name: "automation",
				Key:  "ci",
				Hash: "untrusted-hash",
				Role: control.RoleWrite,
			}},
		},
	})
	if err == nil {
		t.Fatal("an unrecognized submitted token hash was accepted")
	}

	_, err = service.updateGroupSettings(ctx, &updateGroupSettingsInput{
		GroupPathInput: GroupPathInput{AuthInput: credentials, Path: "engineering"},
		Body: updateGroupSettingsBody{
			Name:    "engineering",
			Members: map[string]control.Role{"alice": "invalid"},
		},
	})
	if err == nil {
		t.Fatal("invalid group settings were accepted")
	}

	created, err := service.updateGroupSettings(ctx, &updateGroupSettingsInput{
		GroupPathInput: GroupPathInput{AuthInput: credentials, Path: "engineering"},
		Body: updateGroupSettingsBody{
			Name:        "engineering",
			Description: "Backend services",
			Members:     map[string]control.Role{"alice": control.RoleOwner},
			Tokens: []groupTokenInput{{
				Name:      "automation",
				Key:       "ci",
				NewSecret: "first-secret",
				Role:      control.RoleWrite,
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	token := created.Body.Settings.Tokens[0]
	if token.Hash == "" || token.Hash == "first-secret" ||
		!auth.VerifySecret(token.Hash, "first-secret") {
		t.Fatalf("new token secret was not secured: %#v", token)
	}
	if created.Body.Settings.Repositories == nil {
		t.Fatal("nil repository policies were not normalized")
	}

	preserved, err := service.updateGroupSettings(ctx, &updateGroupSettingsInput{
		GroupPathInput: GroupPathInput{AuthInput: credentials, Path: "engineering"},
		Body: updateGroupSettingsBody{
			Name:        "engineering",
			Description: "Updated description",
			Members:     map[string]control.Role{"alice": control.RoleOwner},
			Tokens: []groupTokenInput{{
				Name: "automation",
				Key:  token.Key,
				Hash: token.Hash,
				Role: control.RoleAdmin,
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if preserved.Body.Settings.Tokens[0].Hash != token.Hash {
		t.Fatal("unchanged token hash was not preserved")
	}
}

func TestUpdateGroupSettingsRenamesGroupAndRepository(t *testing.T) {
	service, credentials, _ := repositoryAPIFixture(t)
	output, err := service.updateGroupSettings(context.Background(), &updateGroupSettingsInput{
		GroupPathInput: GroupPathInput{AuthInput: credentials, Path: "engineering"},
		Body: updateGroupSettingsBody{
			Name:        "platform",
			Description: "Renamed group",
			Members:     map[string]control.Role{"alice": control.RoleOwner},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if output.Body.Path != "platform" || output.Body.Settings.Group != "platform" {
		t.Fatalf("unexpected rename output: %#v", output.Body)
	}
	if _, err = service.Storage.GitPath(repopath.Repository{
		Groups: []string{"platform"},
		Name:   "api",
	}); err != nil {
		t.Fatalf("renamed repository is unavailable: %v", err)
	}
	if _, err = service.Resolver.Controls.Load(context.Background(), "platform"); err != nil {
		t.Fatalf("renamed controls are unavailable: %v", err)
	}
}

func TestSessionEndpointsRejectMissingConfigurationAndCredentials(t *testing.T) {
	ctx := context.Background()
	service := API{}
	if _, err := service.login(ctx, &loginInput{}); err == nil {
		t.Fatal("login succeeded without session configuration")
	}
	if _, err := service.getSession(ctx, &sessionInput{}); err == nil {
		t.Fatal("session lookup succeeded without session configuration")
	}
	if _, err := service.logout(ctx, &struct{}{}); err == nil {
		t.Fatal("logout succeeded without session configuration")
	}

	manager, err := auth.NewEphemeralSessionManager(false)
	if err != nil {
		t.Fatal(err)
	}
	service = API{
		Resolver: &auth.Resolver{Directory: testIdentityProvider{"alice": "secret"}},
		Sessions: manager,
	}
	if _, err = service.login(ctx, &loginInput{
		Body: loginBody{Username: "alice", Password: "wrong"},
	}); err == nil {
		t.Fatal("invalid LDAP credentials were accepted")
	}
	if _, err = service.getSession(ctx, &sessionInput{Cookie: "missing=value"}); err == nil {
		t.Fatal("invalid browser session was accepted")
	}
	login, err := service.login(ctx, &loginInput{
		Body: loginBody{Username: " alice ", Password: "secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	cookie := strings.SplitN(login.SetCookie, ";", 2)[0]
	session, err := service.getSession(ctx, &sessionInput{Cookie: cookie})
	if err != nil || session.Body.Username != "alice" {
		t.Fatalf("session response = %#v, %v", session, err)
	}
	logout, err := service.logout(ctx, &struct{}{})
	if err != nil || !strings.Contains(logout.SetCookie, auth.SessionCookieName+"=") {
		t.Fatalf("logout response = %#v, %v", logout, err)
	}
}

func TestRepositoryLifecycleEndpoints(t *testing.T) {
	service, credentials, _ := repositoryAPIFixture(t)
	ctx := context.Background()

	if _, err := service.createRepository(ctx, &createRepositoryInput{
		RepositoryPathInput: RepositoryPathInput{AuthInput: credentials, Path: "invalid"},
	}); err == nil {
		t.Fatal("repository without a group was created")
	}
	created, err := service.createRepository(ctx, &createRepositoryInput{
		RepositoryPathInput: RepositoryPathInput{
			AuthInput: credentials,
			Path:      "engineering/worker",
		},
		InitializeReadme: true,
		Description:      "Worker service",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Body.Group != "engineering" || created.Body.Name != "worker" {
		t.Fatalf("unexpected create response: %#v", created.Body)
	}
	if _, err = service.createRepository(ctx, &createRepositoryInput{
		RepositoryPathInput: RepositoryPathInput{
			AuthInput: credentials,
			Path:      "engineering/worker",
		},
	}); err == nil {
		t.Fatal("duplicate repository was created")
	}

	if _, err = service.renameRepository(ctx, &renameRepositoryInput{
		RepositoryPathInput: RepositoryPathInput{AuthInput: credentials, Path: "invalid"},
		Body:                renameRepositoryBody{NewName: "renamed"},
	}); err == nil {
		t.Fatal("invalid repository path was renamed")
	}
	if _, err = service.renameRepository(ctx, &renameRepositoryInput{
		RepositoryPathInput: RepositoryPathInput{
			AuthInput: credentials,
			Path:      "engineering/worker",
		},
		Body: renameRepositoryBody{NewName: "renamed.git"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = service.renameRepository(ctx, &renameRepositoryInput{
		RepositoryPathInput: RepositoryPathInput{
			AuthInput: credentials,
			Path:      "engineering/missing",
		},
		Body: renameRepositoryBody{NewName: "other"},
	}); err == nil {
		t.Fatal("missing repository was renamed")
	}

	if _, err = service.deleteRepository(ctx, &RepositoryPathInput{
		AuthInput: credentials,
		Path:      "invalid",
	}); err == nil {
		t.Fatal("invalid repository path was deleted")
	}
	if _, err = service.deleteRepository(ctx, &RepositoryPathInput{
		AuthInput: credentials,
		Path:      "engineering/renamed",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = service.deleteRepository(ctx, &RepositoryPathInput{
		AuthInput: credentials,
		Path:      "engineering/renamed",
	}); err == nil {
		t.Fatal("missing repository was deleted")
	}
}

func TestGroupLifecycleEndpoints(t *testing.T) {
	service, credentials, _ := repositoryAPIFixture(t)
	ctx := context.Background()

	if _, err := service.createGroup(ctx, &createGroupInput{
		GroupPathInput: GroupPathInput{AuthInput: credentials, Path: ""},
	}); err == nil {
		t.Fatal("invalid group path was created")
	}
	root, err := service.createGroup(ctx, &createGroupInput{
		GroupPathInput: GroupPathInput{AuthInput: credentials, Path: "design"},
		Description:    "Design team",
	})
	if err != nil || root.Body.Path != "design" {
		t.Fatalf("create root group = %#v, %v", root, err)
	}
	if _, err = service.createGroup(ctx, &createGroupInput{
		GroupPathInput: GroupPathInput{AuthInput: credentials, Path: "design"},
	}); err == nil {
		t.Fatal("duplicate group was created")
	}
	child, err := service.createGroup(ctx, &createGroupInput{
		GroupPathInput: GroupPathInput{
			AuthInput: credentials,
			Path:      "engineering/backend",
		},
		Description: "Backend team",
	})
	if err != nil || child.Body.Path != "engineering/backend" {
		t.Fatalf("create subgroup = %#v, %v", child, err)
	}

	if _, err = service.renameGroup(ctx, &renameGroupInput{
		GroupPathInput: GroupPathInput{AuthInput: credentials, Path: "bad group"},
		Body:           renameGroupBody{NewPath: "engineering/platform"},
	}); err == nil {
		t.Fatal("invalid group path was renamed")
	}
	if _, err = service.renameGroup(ctx, &renameGroupInput{
		GroupPathInput: GroupPathInput{
			AuthInput: credentials,
			Path:      "engineering/backend",
		},
		Body: renameGroupBody{NewPath: "bad group"},
	}); err == nil {
		t.Fatal("group was renamed to an invalid path")
	}
	if _, err = service.renameGroup(ctx, &renameGroupInput{
		GroupPathInput: GroupPathInput{
			AuthInput: credentials,
			Path:      "engineering/backend",
		},
		Body: renameGroupBody{NewPath: "engineering/platform"},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err = service.deleteGroup(ctx, &GroupPathInput{
		AuthInput: credentials,
		Path:      "bad group",
	}); err == nil {
		t.Fatal("invalid group path was deleted")
	}
	if _, err = service.deleteGroup(ctx, &GroupPathInput{
		AuthInput: credentials,
		Path:      "engineering/platform",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = service.deleteGroup(ctx, &GroupPathInput{
		AuthInput: credentials,
		Path:      "engineering",
	}); err == nil {
		t.Fatal("non-empty group was deleted")
	}
}
