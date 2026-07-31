package httpapi

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/define42/GitOne/internal/auth"
	"github.com/define42/GitOne/internal/control"
	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/runner"
	"github.com/define42/GitOne/internal/storage"
)

func TestRepositoryBuildEndpoints(t *testing.T) {
	service, credentials, commit := repositoryAPIFixture(t)
	buildStore := runner.NewStore(service.Storage.Root)
	service.Builds = &buildStore
	repository := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	build := runner.Job{
		ID:         "build-1",
		Repository: repository.Full(),
		Branch:     "main",
		Commit:     commit,
		Image:      "golang:1.25",
		Status:     runner.StatusSucceeded,
		CreatedAt:  time.Now().UTC(),
	}
	directory := filepath.Join(buildStore.Root, "engineering", "api.build")
	if err := os.MkdirAll(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	contents, err := json.Marshal(build)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, build.ID+".json"), contents, 0o640); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, build.ID+".log"), []byte("tests passed\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	list, err := service.listRepositoryBuilds(context.Background(), &repositoryBuildsInput{
		AuthInput: credentials, Repository: repository.Full(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if list.Body.Repository != repository.Full() ||
		len(list.Body.Builds) != 1 ||
		list.Body.Builds[0].ID != build.ID ||
		list.Body.CanManage {
		t.Fatalf("unexpected build list: %#v", list.Body)
	}
	detail, err := service.getRepositoryBuild(context.Background(), &repositoryBuildInput{
		AuthInput:  credentials,
		Repository: repository.Full(),
		ID:         build.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if detail.Body.Build.ID != build.ID || detail.Body.Log != "tests passed\n" {
		t.Fatalf("unexpected build detail: %#v", detail.Body)
	}

	mux := http.NewServeMux()
	Register(mux, service)
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/repositories/engineering%2Fapi/builds/"+build.ID,
		nil,
	)
	request.SetBasicAuth("alice", "secret")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf(
			"build detail HTTP status: expected %d, got %d: %s",
			http.StatusOK,
			response.Code,
			response.Body.String(),
		)
	}
	var responseBody struct {
		Build runner.Job `json:"build"`
		Log   string     `json:"log"`
	}
	if err = json.Unmarshal(response.Body.Bytes(), &responseBody); err != nil {
		t.Fatal(err)
	}
	if responseBody.Build.ID != build.ID || responseBody.Log != "tests passed\n" {
		t.Fatalf("unexpected HTTP build detail: %#v", responseBody)
	}
}

func TestRepositoryBuildRerunAndCancelEndpoints(t *testing.T) {
	service, credentials, _ := repositoryAPIFixture(t)
	repository := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	commit := commitRunnerBuildConfig(t, service, repository)
	coordinator, err := runner.NewCoordinator(runner.CoordinatorConfig{
		Storage: service.Storage,
		State:   runner.NewStore(service.Storage.Root),
	})
	if err != nil {
		t.Fatal(err)
	}
	service.Coordinator = coordinator
	buildStore := coordinator.Store()
	service.Builds = &buildStore

	original, err := coordinator.Schedule(repository, "main", commit)
	if err != nil || original == nil {
		t.Fatalf("scheduled build = %#v, %v", original, err)
	}
	lease, err := coordinator.Claim("runner-one")
	if err != nil || lease == nil {
		t.Fatalf("claimed build = %#v, %v", lease, err)
	}
	if _, err = coordinator.Complete(repository, original.ID, "runner-one", "tests failed"); err != nil {
		t.Fatal(err)
	}

	list, err := service.listRepositoryBuilds(context.Background(), &repositoryBuildsInput{
		AuthInput: credentials, Repository: repository.Full(),
	})
	if err != nil || !list.Body.CanManage {
		t.Fatalf("build management capability = %v, %v", list.Body.CanManage, err)
	}
	rerun, err := service.rerunRepositoryBuild(context.Background(), &repositoryBuildInput{
		AuthInput: credentials, Repository: repository.Full(), ID: original.ID,
	})
	if err != nil || rerun.Body.Build.Status != runner.StatusQueued ||
		rerun.Body.Build.RerunOf != original.ID {
		t.Fatalf("rerun response = %#v, %v", rerun, err)
	}
	canceled, err := service.cancelRepositoryBuild(context.Background(), &repositoryBuildInput{
		AuthInput: credentials, Repository: repository.Full(), ID: rerun.Body.Build.ID,
	})
	if err != nil || canceled.Body.Build.Status != runner.StatusCanceled {
		t.Fatalf("cancel response = %#v, %v", canceled, err)
	}

	mux := http.NewServeMux()
	Register(mux, service)
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/repositories/engineering%2Fapi/builds/"+original.ID+"/rerun",
		nil,
	)
	request.Header.Set("Authorization", credentials.Authorization)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("rerun HTTP status = %d: %s", response.Code, response.Body.String())
	}

	_, err = service.rerunRepositoryBuild(context.Background(), &repositoryBuildInput{
		AuthInput: credentials, Repository: repository.Full(), ID: "missing",
	})
	requireStatusError(t, err, http.StatusNotFound)
	_, err = service.cancelRepositoryBuild(context.Background(), &repositoryBuildInput{
		AuthInput: credentials, Repository: repository.Full(), ID: original.ID,
	})
	requireStatusError(t, err, http.StatusConflict)

	document, err := service.Resolver.Controls.Load(context.Background(), "engineering")
	if err != nil {
		t.Fatal(err)
	}
	document.Members["bob"] = control.RoleRead
	if err = service.Storage.UpdateGroupControl("engineering", document, "alice"); err != nil {
		t.Fatal(err)
	}
	service.Resolver.Controls.Invalidate("engineering")
	service.Resolver.Directory = testIdentityProvider{
		"alice": "secret",
		"bob":   "bob-secret",
	}
	readRequest := httptest.NewRequest(http.MethodGet, "/", nil)
	readRequest.SetBasicAuth("bob", "bob-secret")
	readCredentials := AuthInput{Authorization: readRequest.Header.Get("Authorization")}
	readList, err := service.listRepositoryBuilds(context.Background(), &repositoryBuildsInput{
		AuthInput: readCredentials, Repository: repository.Full(),
	})
	if err != nil || readList.Body.CanManage {
		t.Fatalf("read-only build capability = %v, %v", readList.Body.CanManage, err)
	}
	_, err = service.rerunRepositoryBuild(context.Background(), &repositoryBuildInput{
		AuthInput: readCredentials, Repository: repository.Full(), ID: original.ID,
	})
	requireStatusError(t, err, http.StatusForbidden)
}

func TestGroupSummariesIncludeEffectiveRole(t *testing.T) {
	service, _, _ := repositoryAPIFixture(t)
	ctx := context.Background()
	if err := service.Storage.CreateGroup(
		"engineering/platform",
		"alice",
		"Platform services",
	); err != nil {
		t.Fatal(err)
	}
	document, err := service.Resolver.Controls.Load(ctx, "engineering")
	if err != nil {
		t.Fatal(err)
	}
	document.Description = "Engineering services"
	document.Members["bob"] = control.RoleDeveloper
	if err = service.Storage.UpdateGroupControl("engineering", document, "alice"); err != nil {
		t.Fatal(err)
	}
	service.Resolver.Controls.Invalidate("engineering")
	service.Resolver.Directory = testIdentityProvider{
		"alice": "secret",
		"bob":   "bob-secret",
	}
	request, err := http.NewRequest(http.MethodGet, "/", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.SetBasicAuth("bob", "bob-secret")
	credentials := AuthInput{Authorization: request.Header.Get("Authorization")}

	groups, err := service.listGroups(ctx, &listGroupsInput{AuthInput: credentials})
	if err != nil {
		t.Fatal(err)
	}
	if len(groups.Body.Groups) != 1 ||
		groups.Body.Groups[0].Path != "engineering" ||
		groups.Body.Groups[0].Description != "Engineering services" ||
		groups.Body.Groups[0].Role != control.RoleDeveloper {
		t.Fatalf("unexpected group summaries: %#v", groups.Body.Groups)
	}

	group, err := service.getGroup(ctx, &GroupPathInput{
		AuthInput: credentials,
		Path:      "engineering",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(group.Body.Subgroups) != 1 ||
		group.Body.Role != control.RoleDeveloper ||
		group.Body.Subgroups[0].Path != "engineering/platform" ||
		group.Body.Subgroups[0].Description != "Platform services" ||
		group.Body.Subgroups[0].Role != control.RoleDeveloper {
		t.Fatalf("unexpected subgroup summaries: %#v", group.Body.Subgroups)
	}
}

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

func TestUpdateGroupSettingsGeneratesRotatesAndPreservesTokenSecrets(t *testing.T) {
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
			Visibility:  "internal",
			LFS: control.LFSPolicy{
				Enabled:             true,
				MaximumObjectBytes:  1024,
				MaximumStorageBytes: 4096,
			},
			Members: map[string]control.Role{"alice": control.RoleOwner},
			Tokens: []groupTokenInput{{
				Name: "automation",
				Key:  "ci",
				Role: control.RoleDeveloper,
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	token := created.Body.Settings.Tokens[0]
	if len(created.Body.GeneratedSecrets) != 1 {
		t.Fatalf("generated secrets = %#v", created.Body.GeneratedSecrets)
	}
	generated := created.Body.GeneratedSecrets[0]
	if generated.Name != token.Name || generated.Key != token.Key ||
		len(generated.Secret) != auth.TokenSecretLength {
		t.Fatalf("unexpected generated token secret: %#v", generated)
	}
	stored, err := service.Resolver.Controls.Load(ctx, "engineering")
	if err != nil {
		t.Fatal(err)
	}
	originalHash := stored.Tokens[0].Hash
	if originalHash == "" || originalHash == generated.Secret ||
		!auth.VerifySecret(originalHash, generated.Secret) {
		t.Fatalf("new token secret was not secured: %#v", stored.Tokens[0])
	}
	if created.Body.Settings.Visibility != "internal" ||
		!created.Body.Settings.LFS.Enabled ||
		created.Body.Settings.LFS.MaximumStorageBytes != 4096 {
		t.Fatalf("group policy was not retained: %#v", created.Body.Settings)
	}

	preserved, err := service.updateGroupSettings(ctx, &updateGroupSettingsInput{
		GroupPathInput: GroupPathInput{AuthInput: credentials, Path: "engineering"},
		Body: updateGroupSettingsBody{
			Name:        "engineering",
			Description: "Updated description",
			Visibility:  created.Body.Settings.Visibility,
			LFS:         created.Body.Settings.LFS,
			Members:     map[string]control.Role{"alice": control.RoleOwner},
			Tokens: []groupTokenInput{{
				Name: "automation",
				Key:  token.Key,
				Role: control.RoleMaintainer,
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(preserved.Body.GeneratedSecrets) != 0 {
		t.Fatalf("unchanged token generated a new secret: %#v", preserved.Body.GeneratedSecrets)
	}
	stored, err = service.Resolver.Controls.Load(ctx, "engineering")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Tokens[0].Hash != originalHash {
		t.Fatal("unchanged token hash was not preserved")
	}

	rotated, err := service.updateGroupSettings(ctx, &updateGroupSettingsInput{
		GroupPathInput: GroupPathInput{AuthInput: credentials, Path: "engineering"},
		Body: updateGroupSettingsBody{
			Name:        "engineering",
			Description: preserved.Body.Settings.Description,
			Visibility:  preserved.Body.Settings.Visibility,
			LFS:         preserved.Body.Settings.LFS,
			Members:     map[string]control.Role{"alice": control.RoleOwner},
			Tokens: []groupTokenInput{{
				Name:       "automation",
				Key:        token.Key,
				Role:       control.RoleMaintainer,
				Regenerate: true,
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rotated.Body.GeneratedSecrets) != 1 {
		t.Fatalf("rotated secrets = %#v", rotated.Body.GeneratedSecrets)
	}
	rotatedSecret := rotated.Body.GeneratedSecrets[0].Secret
	if rotatedSecret == generated.Secret || len(rotatedSecret) != auth.TokenSecretLength {
		t.Fatalf("unexpected rotated secret: %q", rotatedSecret)
	}
	stored, err = service.Resolver.Controls.Load(ctx, "engineering")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Tokens[0].Hash == originalHash ||
		!auth.VerifySecret(stored.Tokens[0].Hash, rotatedSecret) ||
		auth.VerifySecret(stored.Tokens[0].Hash, generated.Secret) {
		t.Fatal("token secret was not rotated")
	}
}

func TestUpdateGroupSettingsRequiresOwnerForProtectedFields(t *testing.T) {
	service, ownerCredentials, _ := repositoryAPIFixture(t)
	ctx := context.Background()
	current, err := service.Resolver.Controls.Load(ctx, "engineering")
	if err != nil {
		t.Fatal(err)
	}
	current.Members["bob"] = control.RoleMaintainer
	if err = service.Storage.UpdateGroupControl("engineering", current, "alice"); err != nil {
		t.Fatal(err)
	}
	service.Resolver.Controls.Invalidate("engineering")
	service.Resolver.Directory = testIdentityProvider{
		"alice": "secret",
		"bob":   "bob-secret",
	}
	request, err := http.NewRequest(http.MethodGet, "/", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.SetBasicAuth("bob", "bob-secret")
	maintainerCredentials := AuthInput{Authorization: request.Header.Get("Authorization")}

	settingsBody := func() updateGroupSettingsBody {
		t.Helper()
		document, loadErr := service.Resolver.Controls.Load(ctx, "engineering")
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		tokens := make([]groupTokenInput, 0, len(document.Tokens))
		for _, token := range document.Tokens {
			tokens = append(tokens, groupTokenInput{
				Name:      token.Name,
				Key:       token.Key,
				Role:      token.Role,
				ExpiresAt: token.ExpiresAt,
				Disabled:  token.Disabled,
			})
		}
		return updateGroupSettingsBody{
			Name:        "engineering",
			Description: document.Description,
			Inherit:     document.Inherit,
			Visibility:  document.Visibility,
			LFS:         document.LFS,
			Members:     maps.Clone(document.Members),
			Tokens:      tokens,
		}
	}
	update := func(credentials AuthInput, body updateGroupSettingsBody) error {
		t.Helper()
		_, updateErr := service.updateGroupSettings(ctx, &updateGroupSettingsInput{
			GroupPathInput: GroupPathInput{
				AuthInput: credentials,
				Path:      "engineering",
			},
			Body: body,
		})
		return updateErr
	}

	maintainerUpdate := settingsBody()
	maintainerUpdate.Description = "Maintainers may change ordinary settings"
	maintainerUpdate.Tokens = append(maintainerUpdate.Tokens, groupTokenInput{
		Name: "automation",
		Key:  "ci",
		Role: control.RoleMaintainer,
	})
	if err = update(maintainerCredentials, maintainerUpdate); err != nil {
		t.Fatalf("maintainer ordinary settings update failed: %v", err)
	}

	for _, test := range []struct {
		name   string
		change func(*updateGroupSettingsBody)
	}{
		{
			name: "members",
			change: func(body *updateGroupSettingsBody) {
				body.Members["carol"] = control.RoleRead
			},
		},
		{
			name: "visibility",
			change: func(body *updateGroupSettingsBody) {
				body.Visibility = "internal"
			},
		},
		{
			name: "LFS policy",
			change: func(body *updateGroupSettingsBody) {
				body.LFS.MaximumObjectBytes = 1024
			},
		},
		{
			name: "owner token",
			change: func(body *updateGroupSettingsBody) {
				body.Tokens = append(body.Tokens, groupTokenInput{
					Name: "owner automation",
					Key:  "owner-ci",
					Role: control.RoleOwner,
				})
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := settingsBody()
			test.change(&body)
			if updateErr := update(maintainerCredentials, body); updateErr == nil {
				t.Fatalf("maintainer changed owner-only setting %s", test.name)
			}
			persisted, loadErr := service.Resolver.Controls.Load(ctx, "engineering")
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if !maps.Equal(persisted.Members, current.Members) ||
				persisted.Visibility != current.Visibility ||
				persisted.LFS != current.LFS ||
				len(persisted.Tokens) != 1 ||
				persisted.Tokens[0].Role != control.RoleMaintainer {
				t.Fatalf("denied update changed settings: %#v", persisted)
			}
		})
	}

	ownerUpdate := settingsBody()
	ownerUpdate.Members["carol"] = control.RoleRead
	ownerUpdate.Visibility = "internal"
	ownerUpdate.LFS.MaximumObjectBytes = 1024
	updated, err := service.updateGroupSettings(ctx, &updateGroupSettingsInput{
		GroupPathInput: GroupPathInput{
			AuthInput: ownerCredentials,
			Path:      "engineering",
		},
		Body: ownerUpdate,
	})
	if err != nil {
		t.Fatalf("owner protected settings update failed: %v", err)
	}
	if updated.Body.Settings.Members["carol"] != control.RoleRead ||
		updated.Body.Settings.Visibility != "internal" ||
		updated.Body.Settings.LFS.MaximumObjectBytes != 1024 {
		t.Fatalf("owner settings changes were not retained: %#v", updated.Body.Settings)
	}
}

func TestUpdateGroupSettingsRenamesGroupAndRepository(t *testing.T) {
	service, credentials, _ := repositoryAPIFixture(t)
	output, err := service.updateGroupSettings(context.Background(), &updateGroupSettingsInput{
		GroupPathInput: GroupPathInput{AuthInput: credentials, Path: "engineering"},
		Body: updateGroupSettingsBody{
			Name:        "platform",
			Description: "Renamed group",
			Visibility:  "private",
			LFS:         control.LFSPolicy{Enabled: true},
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

func TestUpdateGroupSettingsWaitsAndReauthorizesUnderOperationLock(t *testing.T) {
	service, credentials, _ := repositoryAPIFixture(t)
	current, err := service.Resolver.Controls.Load(context.Background(), "engineering")
	if err != nil {
		t.Fatal(err)
	}
	release, err := service.reviewStore().AcquireOperationLock()
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		close(started)
		_, updateErr := service.updateGroupSettings(
			context.Background(),
			&updateGroupSettingsInput{
				GroupPathInput: GroupPathInput{
					AuthInput: credentials,
					Path:      "engineering",
				},
				Body: updateGroupSettingsBody{
					Name:        "engineering",
					Description: "must not be committed",
					Members:     map[string]control.Role{"alice": control.RoleOwner},
				},
			},
		)
		result <- updateErr
	}()
	<-started
	select {
	case err = <-result:
		_ = release()
		t.Fatalf("group update completed while operation lock was held: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	current.Members = map[string]control.Role{"bob": control.RoleOwner}
	if err = service.Storage.UpdateGroupControlLocked("engineering", current, "bob"); err != nil {
		_ = release()
		t.Fatal(err)
	}
	service.Resolver.Controls.Invalidate("engineering")
	if err = release(); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-result:
		if err == nil {
			t.Fatal("group update retained stale authorization after waiting")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("group update did not resume after operation lock release")
	}
	persisted, err := service.Resolver.Controls.Load(context.Background(), "engineering")
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Description == "must not be committed" ||
		persisted.Members["bob"] != control.RoleOwner {
		t.Fatalf("stale group update changed control state: %#v", persisted)
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

func TestMaintainerCreatesSubgroupWithInheritedRole(t *testing.T) {
	service, ownerCredentials, _ := repositoryAPIFixture(t)
	ctx := context.Background()

	parent, err := service.Resolver.Controls.Load(ctx, "engineering")
	if err != nil {
		t.Fatal(err)
	}
	parent.Members["bob"] = control.RoleMaintainer
	if err = service.Storage.UpdateGroupControl("engineering", parent, "alice"); err != nil {
		t.Fatal(err)
	}
	service.Resolver.Controls.Invalidate("engineering")
	service.Resolver.Directory = testIdentityProvider{
		"alice": "secret",
		"bob":   "bob-secret",
	}
	request, err := http.NewRequest(http.MethodGet, "/", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.SetBasicAuth("bob", "bob-secret")
	maintainerCredentials := AuthInput{Authorization: request.Header.Get("Authorization")}

	if _, err = service.createGroup(ctx, &createGroupInput{
		GroupPathInput: GroupPathInput{
			AuthInput: maintainerCredentials,
			Path:      "engineering/backend",
		},
		Description: "Backend team",
	}); err != nil {
		t.Fatal(err)
	}

	child, err := service.Resolver.Controls.Load(ctx, "engineering/backend")
	if err != nil {
		t.Fatal(err)
	}
	if !child.Inherit {
		t.Fatal("new subgroup does not inherit access")
	}
	if len(child.Members) != 0 {
		t.Fatalf("new subgroup has direct members: %#v", child.Members)
	}

	creator, err := service.authorizePrincipal(
		ctx,
		maintainerCredentials,
		"engineering/backend",
		control.RoleMaintainer,
	)
	if err != nil {
		t.Fatal(err)
	}
	if creator.Role != control.RoleMaintainer || creator.Group != "engineering" {
		t.Fatalf("creator role = %#v", creator)
	}
	inheritedOwner, err := service.authorizePrincipal(
		ctx,
		ownerCredentials,
		"engineering/backend",
		control.RoleOwner,
	)
	if err != nil {
		t.Fatal(err)
	}
	if inheritedOwner.Role != control.RoleOwner || inheritedOwner.Group != "engineering" {
		t.Fatalf("owner role = %#v", inheritedOwner)
	}

	if _, err = service.updateGroupSettings(ctx, &updateGroupSettingsInput{
		GroupPathInput: GroupPathInput{
			AuthInput: maintainerCredentials,
			Path:      "engineering/backend",
		},
		Body: updateGroupSettingsBody{
			Name:        "backend",
			Description: child.Description,
			Inherit:     false,
			Visibility:  child.Visibility,
			LFS:         child.LFS,
			Members:     map[string]control.Role{},
			Tokens:      []groupTokenInput{},
		},
	}); err == nil {
		t.Fatal("ownerless subgroup disabled inheritance")
	}
}

func TestRenameGroupRequiresMaintainerAccessToChangedParents(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := storage.Store{Root: root}
	for _, group := range []struct {
		path  string
		owner string
	}{
		{path: "source", owner: "bob"},
		{path: "source/team", owner: "alice"},
		{path: "destination", owner: "carol"},
	} {
		if err := store.CreateGroup(group.path, group.owner, ""); err != nil {
			t.Fatal(err)
		}
	}

	controls := control.NewStore(root)
	service := API{
		Storage: store,
		Resolver: &auth.Resolver{
			Controls: controls,
			Directory: testIdentityProvider{
				"alice": "alice-secret",
				"bob":   "bob-secret",
				"carol": "carol-secret",
			},
		},
	}
	request, err := http.NewRequest(http.MethodGet, "/", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.SetBasicAuth("alice", "alice-secret")
	credentials := AuthInput{Authorization: request.Header.Get("Authorization")}
	move := func() error {
		_, moveErr := service.renameGroup(ctx, &renameGroupInput{
			GroupPathInput: GroupPathInput{
				AuthInput: credentials,
				Path:      "source/team",
			},
			Body: renameGroupBody{NewPath: "destination/team"},
		})
		return moveErr
	}
	setRole := func(group, username string, role control.Role) {
		t.Helper()
		document, loadErr := controls.Load(ctx, group)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if role == "" {
			delete(document.Members, username)
		} else {
			document.Members[username] = role
		}
		if updateErr := store.UpdateGroupControl(group, document, "test"); updateErr != nil {
			t.Fatal(updateErr)
		}
		controls.Invalidate(group)
	}

	setRole("destination", "alice", control.RoleMaintainer)
	if err = move(); err == nil {
		t.Fatal("group moved without source-parent maintainer access")
	}
	if _, err = controls.Load(ctx, "source/team"); err != nil {
		t.Fatalf("source group changed after denied move: %v", err)
	}

	setRole("source", "alice", control.RoleMaintainer)
	setRole("destination", "alice", "")
	if err = move(); err == nil {
		t.Fatal("group moved without destination-parent maintainer access")
	}
	if _, err = controls.Load(ctx, "source/team"); err != nil {
		t.Fatalf("source group changed after denied move: %v", err)
	}

	setRole("destination", "alice", control.RoleMaintainer)
	if err = move(); err != nil {
		t.Fatalf("group move with both parent permissions failed: %v", err)
	}
	if _, err = controls.Load(ctx, "source/team"); err == nil {
		t.Fatal("source group still exists after authorized move")
	}
	moved, err := controls.Load(ctx, "destination/team")
	if err != nil {
		t.Fatalf("destination group is unavailable after authorized move: %v", err)
	}
	if moved.Group != "destination/team" ||
		moved.Members["alice"] != control.RoleOwner {
		t.Fatalf("unexpected moved group control: %#v", moved)
	}
}
