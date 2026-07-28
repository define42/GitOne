package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
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
	"github.com/define42/GitOne/internal/storage"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	gittransport "github.com/go-git/go-git/v5/plumbing/transport/http"
)

type staticDirectory map[string]string

var testLDAPDirectory = staticDirectory{"alice": "secret"}

func (d staticDirectory) Authenticate(
	_ context.Context,
	username string,
	password string,
) (string, error) {
	if expected, ok := d[username]; !ok || expected != password {
		return "", errors.New("invalid credentials")
	}
	return username, nil
}

func TestCreateGroupUsesAuthenticatedUserAsOwner(t *testing.T) {
	root := t.TempDir()
	handler := New(Config{
		Root:      root,
		Directory: testLDAPDirectory,
	})
	request := httptest.NewRequest(http.MethodPost, "/api/groups/engineering?description=Engineering%20projects", nil)
	request.SetBasicAuth("alice", "secret")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("create group: status %d: %s", response.Code, response.Body.String())
	}
	document, err := control.NewStore(root).Load(context.Background(), "engineering")
	if err != nil {
		t.Fatalf("load control document: %v", err)
	}
	if document.Members["alice"] != control.RoleOwner {
		t.Fatalf("authenticated user is not owner: %#v", document.Members)
	}
	if document.Description != "Engineering projects" {
		t.Fatalf("unexpected group description: %q", document.Description)
	}
	if len(document.Tokens) != 0 {
		t.Fatalf("expected no generated tokens, got %#v", document.Tokens)
	}

	subgroupRequest := httptest.NewRequest(http.MethodPost, "/api/groups/engineering%2Fbackend", nil)
	subgroupRequest.SetBasicAuth("alice", "secret")
	subgroupResponse := httptest.NewRecorder()
	handler.ServeHTTP(subgroupResponse, subgroupRequest)
	if subgroupResponse.Code != http.StatusCreated {
		t.Fatalf("create subgroup: status %d: %s", subgroupResponse.Code, subgroupResponse.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "engineering", "backend")); err != nil {
		t.Fatalf("subgroup was not created: %v", err)
	}
}

func TestLDAPLoginCreatesSecureBrowserSession(t *testing.T) {
	root := t.TempDir()
	store := storage.Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	sessions, err := auth.NewSessionManager(auth.SessionConfig{
		HashKey:  []byte(strings.Repeat("h", 64)),
		BlockKey: []byte(strings.Repeat("b", 32)),
		MaxAge:   time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := New(Config{
		Root:      root,
		Directory: staticDirectory{"alice": "ldap-secret"},
		Sessions:  sessions,
	})

	invalid := httptest.NewRequest(
		http.MethodPost,
		"/api/session",
		strings.NewReader(`{"username":"alice","password":"wrong"}`),
	)
	invalid.Header.Set("Content-Type", "application/json")
	invalidResponse := httptest.NewRecorder()
	handler.ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusUnauthorized ||
		invalidResponse.Header().Get("Set-Cookie") != "" {
		t.Fatalf("invalid LDAP login returned %d with cookie %q", invalidResponse.Code, invalidResponse.Header().Get("Set-Cookie"))
	}

	login := httptest.NewRequest(
		http.MethodPost,
		"/api/session",
		strings.NewReader(`{"username":"alice","password":"ldap-secret"}`),
	)
	login.Header.Set("Content-Type", "application/json")
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("LDAP login returned %d: %s", loginResponse.Code, loginResponse.Body.String())
	}
	setCookie := loginResponse.Header().Get("Set-Cookie")
	if !strings.Contains(setCookie, "HttpOnly") ||
		!strings.Contains(setCookie, "SameSite=Strict") ||
		strings.Contains(setCookie, "alice") ||
		strings.Contains(setCookie, "ldap-secret") {
		t.Fatalf("unexpected session cookie: %s", setCookie)
	}
	cookies := loginResponse.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != auth.SessionCookieName {
		t.Fatalf("login did not issue the GitOne session cookie: %#v", cookies)
	}

	group := httptest.NewRequest(http.MethodGet, "/api/groups/engineering", nil)
	group.AddCookie(cookies[0])
	groupResponse := httptest.NewRecorder()
	handler.ServeHTTP(groupResponse, group)
	if groupResponse.Code != http.StatusOK {
		t.Fatalf("session cookie did not authorize API request: %d: %s", groupResponse.Code, groupResponse.Body.String())
	}

	current := httptest.NewRequest(http.MethodGet, "/api/session", nil)
	current.AddCookie(cookies[0])
	currentResponse := httptest.NewRecorder()
	handler.ServeHTTP(currentResponse, current)
	if currentResponse.Code != http.StatusOK ||
		!strings.Contains(currentResponse.Body.String(), `"username":"alice"`) {
		t.Fatalf("unexpected current session response: %d: %s", currentResponse.Code, currentResponse.Body.String())
	}

	logout := httptest.NewRequest(http.MethodDelete, "/api/session", nil)
	logout.AddCookie(cookies[0])
	logoutResponse := httptest.NewRecorder()
	handler.ServeHTTP(logoutResponse, logout)
	if logoutResponse.Code != http.StatusNoContent ||
		!strings.Contains(logoutResponse.Header().Get("Set-Cookie"), "Max-Age=0") {
		t.Fatalf("logout did not clear the session: %d %q", logoutResponse.Code, logoutResponse.Header().Get("Set-Cookie"))
	}
}

func TestLDAPUserCanCreateRootGroupButNotUnauthorizedSubgroup(t *testing.T) {
	root := t.TempDir()
	store := storage.Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	sessions, err := auth.NewSessionManager(auth.SessionConfig{
		HashKey:  []byte(strings.Repeat("h", 64)),
		BlockKey: []byte(strings.Repeat("b", 32)),
		MaxAge:   time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := New(Config{
		Root:      root,
		Directory: staticDirectory{"bob": "ldap-secret"},
		Sessions:  sessions,
	})
	login := httptest.NewRequest(
		http.MethodPost,
		"/api/session",
		strings.NewReader(`{"username":"bob","password":"ldap-secret"}`),
	)
	login.Header.Set("Content-Type", "application/json")
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("LDAP login returned %d: %s", loginResponse.Code, loginResponse.Body.String())
	}
	cookies := loginResponse.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("LDAP login returned unexpected cookies: %#v", cookies)
	}

	createRoot := httptest.NewRequest(
		http.MethodPost,
		"/api/groups/design?description=Product%20design",
		nil,
	)
	createRoot.AddCookie(cookies[0])
	createRootResponse := httptest.NewRecorder()
	handler.ServeHTTP(createRootResponse, createRoot)
	if createRootResponse.Code != http.StatusCreated {
		t.Fatalf("root group creation returned %d: %s", createRootResponse.Code, createRootResponse.Body.String())
	}
	document, err := control.NewStore(root).Load(context.Background(), "design")
	if err != nil {
		t.Fatal(err)
	}
	if document.Members["bob"] != control.RoleOwner ||
		document.Description != "Product design" {
		t.Fatalf("LDAP group creator did not become owner: %#v", document)
	}

	createSubgroup := httptest.NewRequest(
		http.MethodPost,
		"/api/groups/engineering%2Fbackend",
		nil,
	)
	createSubgroup.AddCookie(cookies[0])
	createSubgroupResponse := httptest.NewRecorder()
	handler.ServeHTTP(createSubgroupResponse, createSubgroup)
	if createSubgroupResponse.Code != http.StatusForbidden {
		t.Fatalf(
			"unauthorized subgroup creation returned %d: %s",
			createSubgroupResponse.Code,
			createSubgroupResponse.Body.String(),
		)
	}
}

func TestRepositoryVisibilityAndTokenScopeAreEnforced(t *testing.T) {
	root := t.TempDir()
	store := storage.Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"public", "internal", "api", "web"} {
		if err := store.CreateRepository(
			repopath.Repository{Groups: []string{"engineering"}, Name: name},
			storage.CreateRepositoryOptions{InitializeReadme: true, Author: "alice"},
		); err != nil {
			t.Fatal(err)
		}
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
	document.Repositories["public"] = control.RepositoryPolicy{Visibility: "public"}
	document.Repositories["internal"] = control.RepositoryPolicy{Visibility: "internal"}
	document.Tokens = []control.Token{{
		Name:         "deploy",
		Key:          "ci",
		Hash:         tokenHash,
		Role:         control.RoleAdmin,
		Repositories: []string{"api"},
	}}
	if err = store.UpdateGroupControl("engineering", document, "alice"); err != nil {
		t.Fatal(err)
	}
	if err = store.CreateGroup("community", "bob", ""); err != nil {
		t.Fatal(err)
	}
	community, err := controls.Load(context.Background(), "community")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.UpdateGroupControl("community", community, "bob"); err != nil {
		t.Fatal(err)
	}
	handler := New(Config{
		Root: root,
		Directory: staticDirectory{
			"alice": "secret",
			"bob":   "bob-secret",
		},
	})

	request := httptest.NewRequest(
		http.MethodGet,
		"/engineering/public.git/info/refs?service=git-upload-pack",
		nil,
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("public repository returned %d: %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(
		http.MethodPost,
		"/engineering/api.git/info/lfs/objects/batch",
		strings.NewReader(`{"operation":"download","objects":[]}`),
	)
	request.SetBasicAuth("alice", "secret")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("default LFS policy returned %d: %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(
		http.MethodPost,
		"/engineering/public.git/info/lfs/objects/batch",
		strings.NewReader(`{"operation":"download","objects":[]}`),
	)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("explicitly disabled LFS returned %d: %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(
		http.MethodGet,
		"/engineering/internal.git/info/refs?service=git-upload-pack",
		nil,
	)
	request.SetBasicAuth("bob", "bob-secret")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("internal repository returned %d: %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(
		http.MethodGet,
		"/engineering/api.git/info/refs?service=git-upload-pack",
		nil,
	)
	request.SetBasicAuth("ci", "ci-secret")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("scoped repository returned %d: %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(
		http.MethodGet,
		"/engineering/web.git/info/refs?service=git-upload-pack",
		nil,
	)
	request.SetBasicAuth("ci", "ci-secret")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("out-of-scope repository returned %d: %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/groups/engineering", nil)
	request.SetBasicAuth("ci", "ci-secret")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("group detail returned %d: %s", response.Code, response.Body.String())
	}
	var detail struct {
		Repositories []struct {
			Name string `json:"name"`
		} `json:"repositories"`
	}
	if err = json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Repositories) != 1 || detail.Repositories[0].Name != "api" {
		t.Fatalf("scoped token saw unexpected repositories: %#v", detail.Repositories)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/repositories/engineering%2Fother", nil)
	request.SetBasicAuth("ci", "ci-secret")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("out-of-scope repository creation returned %d: %s", response.Code, response.Body.String())
	}
}

func TestGroupSettingsUpdateControlAndRenameDescendants(t *testing.T) {
	root := t.TempDir()
	handler := New(Config{
		Root: root,
		Directory: staticDirectory{
			"alice": "secret",
			"bob":   "bob-secret",
		},
	})
	for _, path := range []string{"engineering", "engineering%2Fbackend"} {
		request := httptest.NewRequest(http.MethodPost, "/api/groups/"+path, nil)
		request.SetBasicAuth("alice", "secret")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusCreated {
			t.Fatalf("create %s: status %d: %s", path, response.Code, response.Body.String())
		}
	}

	settingsRequest := httptest.NewRequest(http.MethodGet, "/api/groups/engineering/settings", nil)
	settingsRequest.SetBasicAuth("alice", "secret")
	settingsResponse := httptest.NewRecorder()
	handler.ServeHTTP(settingsResponse, settingsRequest)
	if settingsResponse.Code != http.StatusOK {
		t.Fatalf("get settings: status %d: %s", settingsResponse.Code, settingsResponse.Body.String())
	}
	var original control.Document
	if err := json.Unmarshal(settingsResponse.Body.Bytes(), &original); err != nil {
		t.Fatal(err)
	}
	if original.Group != "engineering" || original.Members["alice"] != control.RoleOwner {
		t.Fatalf("unexpected original settings: %#v", original)
	}

	body := `{
		"name": "product",
		"description": "Product engineering",
		"inherit": false,
		"members": {
			"alice": "owner",
			"bob": "read"
		},
		"tokens": [{
			"name": "deploy",
			"key": "ci",
			"newSecret": "deploy-secret",
			"role": "write",
			"repositories": ["api"],
			"disabled": true
		}],
		"repositories": {
			"api": {
				"visibility": "private",
				"lfs": {
					"enabled": true,
					"maximumObjectBytes": 1024,
					"maximumStorageBytes": 4096
				}
			}
		}
	}`
	updateRequest := httptest.NewRequest(
		http.MethodPut,
		"/api/groups/engineering/settings",
		strings.NewReader(body),
	)
	updateRequest.Header.Set("Content-Type", "application/json")
	updateRequest.SetBasicAuth("alice", "secret")
	updateResponse := httptest.NewRecorder()
	handler.ServeHTTP(updateResponse, updateRequest)
	if updateResponse.Code != http.StatusOK {
		t.Fatalf("update settings: status %d: %s", updateResponse.Code, updateResponse.Body.String())
	}

	controls := control.NewStore(root)
	updated, err := controls.Load(context.Background(), "product")
	if err != nil {
		t.Fatal(err)
	}
	descendant, err := controls.Load(context.Background(), "product/backend")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Group != "product" ||
		updated.Description != "Product engineering" ||
		updated.Inherit ||
		updated.Members["bob"] != control.RoleRead ||
		len(updated.Tokens) != 1 ||
		!strings.HasPrefix(updated.Tokens[0].Hash, "$argon2id$") ||
		!updated.Repositories["api"].LFS.Enabled {
		t.Fatalf("unexpected updated settings: %#v", updated)
	}
	if descendant.Group != "product/backend" {
		t.Fatalf("descendant control group was not renamed: %#v", descendant)
	}

	forbidden := httptest.NewRequest(http.MethodGet, "/api/groups/product/settings", nil)
	forbidden.SetBasicAuth("bob", "bob-secret")
	forbiddenResponse := httptest.NewRecorder()
	handler.ServeHTTP(forbiddenResponse, forbidden)
	if forbiddenResponse.Code != http.StatusForbidden {
		t.Fatalf("read-only member accessed settings: status %d", forbiddenResponse.Code)
	}
}

func TestLegacyCreateGroupEndpointIsRemoved(t *testing.T) {
	root := t.TempDir()
	handler := New(Config{
		Root:      root,
		Directory: testLDAPDirectory,
	})
	request := httptest.NewRequest(http.MethodPost, "/api/groups", strings.NewReader("path=engineering"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.SetBasicAuth("alice", "secret")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code >= http.StatusOK && response.Code < http.StatusMultipleChoices {
		t.Fatalf("legacy endpoint unexpectedly succeeded with status %d: %s", response.Code, response.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "engineering")); !os.IsNotExist(err) {
		t.Fatalf("legacy endpoint unexpectedly created a group: %v", err)
	}
}

func TestCreateRepositoryFromPath(t *testing.T) {
	root := t.TempDir()
	handler := New(Config{
		Root:      root,
		Directory: testLDAPDirectory,
	})

	createGroup := httptest.NewRequest(http.MethodPost, "/api/groups/engineering", nil)
	createGroup.SetBasicAuth("alice", "secret")
	groupResponse := httptest.NewRecorder()
	handler.ServeHTTP(groupResponse, createGroup)
	if groupResponse.Code != http.StatusCreated {
		t.Fatalf("create group: status %d: %s", groupResponse.Code, groupResponse.Body.String())
	}

	createRepository := httptest.NewRequest(http.MethodPost, "/api/repositories/engineering%2Fapi?initializeReadme=true&description=Engineering%20API", nil)
	createRepository.SetBasicAuth("alice", "secret")
	repositoryResponse := httptest.NewRecorder()
	handler.ServeHTTP(repositoryResponse, createRepository)
	if repositoryResponse.Code != http.StatusCreated {
		t.Fatalf("create repository: status %d: %s", repositoryResponse.Code, repositoryResponse.Body.String())
	}

	if _, err := os.Stat(filepath.Join(root, "engineering", "api.git")); err != nil {
		t.Fatalf("repository was not created: %v", err)
	}
	storedRepository, err := git.PlainOpen(filepath.Join(root, "engineering", "api.git"))
	if err != nil {
		t.Fatal(err)
	}
	head, err := storedRepository.Head()
	if err != nil {
		t.Fatalf("repository was not initialized: %v", err)
	}
	initialCommit, err := storedRepository.CommitObject(head.Hash())
	if err != nil {
		t.Fatal(err)
	}
	readme, err := initialCommit.File("README.md")
	if err != nil {
		t.Fatal(err)
	}
	readmeContents, err := readme.Contents()
	if err != nil {
		t.Fatal(err)
	}
	if readmeContents != "api\n" {
		t.Fatalf("unexpected README.md contents: %q", readmeContents)
	}
	metadata, err := initialCommit.File(".gitone.json")
	if err != nil {
		t.Fatal(err)
	}
	metadataContents, err := metadata.Contents()
	if err != nil {
		t.Fatal(err)
	}
	if metadataContents != "{\n  \"description\": \"Engineering API\"\n}\n" {
		t.Fatalf("unexpected .gitone.json contents: %q", metadataContents)
	}

	unauthenticatedGit := httptest.NewRequest(http.MethodGet, "/engineering/api.git/info/refs?service=git-upload-pack", nil)
	unauthenticatedGit.SetBasicAuth("alice", "")
	unauthenticatedGitResponse := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticatedGitResponse, unauthenticatedGit)
	if unauthenticatedGitResponse.Code != http.StatusUnauthorized {
		t.Fatalf("expected Git authentication challenge, got status %d: %s", unauthenticatedGitResponse.Code, unauthenticatedGitResponse.Body.String())
	}
	if unauthenticatedGitResponse.Header().Get("WWW-Authenticate") != `Basic realm="GitOne"` {
		t.Fatalf("unexpected Git authentication challenge: %q", unauthenticatedGitResponse.Header().Get("WWW-Authenticate"))
	}

	authenticatedGit := httptest.NewRequest(http.MethodGet, "/engineering/api.git/info/refs?service=git-upload-pack", nil)
	authenticatedGit.SetBasicAuth("alice", "secret")
	authenticatedGitResponse := httptest.NewRecorder()
	handler.ServeHTTP(authenticatedGitResponse, authenticatedGit)
	if authenticatedGitResponse.Code != http.StatusOK {
		t.Fatalf("authenticated Git read: status %d: %s", authenticatedGitResponse.Code, authenticatedGitResponse.Body.String())
	}

	renameRepository := httptest.NewRequest(http.MethodPatch, "/api/repositories/engineering%2Fapi", strings.NewReader(`{"newName":"service"}`))
	renameRepository.Header.Set("Content-Type", "application/json")
	renameRepository.SetBasicAuth("alice", "secret")
	renameRepositoryResponse := httptest.NewRecorder()
	handler.ServeHTTP(renameRepositoryResponse, renameRepository)
	if renameRepositoryResponse.Code != http.StatusNoContent {
		t.Fatalf("rename repository: status %d: %s", renameRepositoryResponse.Code, renameRepositoryResponse.Body.String())
	}

	deleteRepository := httptest.NewRequest(http.MethodDelete, "/api/repositories/engineering%2Fservice", nil)
	deleteRepository.SetBasicAuth("alice", "secret")
	deleteRepositoryResponse := httptest.NewRecorder()
	handler.ServeHTTP(deleteRepositoryResponse, deleteRepository)
	if deleteRepositoryResponse.Code != http.StatusNoContent {
		t.Fatalf("delete repository: status %d: %s", deleteRepositoryResponse.Code, deleteRepositoryResponse.Body.String())
	}

	renameGroup := httptest.NewRequest(http.MethodPatch, "/api/groups/engineering", strings.NewReader(`{"newPath":"platform"}`))
	renameGroup.Header.Set("Content-Type", "application/json")
	renameGroup.SetBasicAuth("alice", "secret")
	renameGroupResponse := httptest.NewRecorder()
	handler.ServeHTTP(renameGroupResponse, renameGroup)
	if renameGroupResponse.Code != http.StatusNoContent {
		t.Fatalf("rename group: status %d: %s", renameGroupResponse.Code, renameGroupResponse.Body.String())
	}

	deleteGroup := httptest.NewRequest(http.MethodDelete, "/api/groups/platform", nil)
	deleteGroup.SetBasicAuth("alice", "secret")
	deleteGroupResponse := httptest.NewRecorder()
	handler.ServeHTTP(deleteGroupResponse, deleteGroup)
	if deleteGroupResponse.Code != http.StatusNoContent {
		t.Fatalf("delete group: status %d: %s", deleteGroupResponse.Code, deleteGroupResponse.Body.String())
	}
}

func TestCloneRepositoryInitializedWithReadme(t *testing.T) {
	handler := New(Config{
		Root:      t.TempDir(),
		Directory: testLDAPDirectory,
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	create := func(path string) {
		t.Helper()
		request, err := http.NewRequest(http.MethodPost, server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.SetBasicAuth("alice", "secret")
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(response.Body)
		closeErr := response.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
		if response.StatusCode != http.StatusCreated {
			t.Fatalf("%s: expected status %d, got %d: %s", path, http.StatusCreated, response.StatusCode, body)
		}
	}

	create("/api/groups/doh")
	create("/api/groups/doh%2Fwhaat")
	create("/api/repositories/doh%2Fwhaat%2Fhello?initializeReadme=true&description=Hello%20repository")

	checkout := filepath.Join(t.TempDir(), "hello")
	credentials := &gittransport.BasicAuth{
		Username: "alice",
		Password: "secret",
	}
	clonedRepository, err := git.PlainClone(checkout, false, &git.CloneOptions{
		URL:  server.URL + "/doh/whaat/hello.git",
		Auth: credentials,
	})
	if err != nil {
		t.Fatalf("clone repository: %v", err)
	}
	head, err := clonedRepository.Head()
	if err != nil {
		t.Fatal(err)
	}
	if head.Name().Short() != "main" {
		t.Fatalf("expected checked-out branch main, got %q", head.Name().Short())
	}
	gitDirectory, err := os.Stat(filepath.Join(checkout, ".git"))
	if err != nil {
		t.Fatalf("cloned Git repository does not exist: %v", err)
	}
	if !gitDirectory.IsDir() {
		t.Fatal("cloned .git path is not a directory")
	}
	readme, err := os.ReadFile(filepath.Join(checkout, "README.md"))
	if err != nil {
		t.Fatalf("cloned README.md does not exist: %v", err)
	}
	if string(readme) != "hello\n" {
		t.Fatalf("unexpected cloned README.md contents: %q", readme)
	}
	metadata, err := os.ReadFile(filepath.Join(checkout, ".gitone.json"))
	if err != nil {
		t.Fatalf("cloned .gitone.json does not exist: %v", err)
	}
	if string(metadata) != "{\n  \"description\": \"Hello repository\"\n}\n" {
		t.Fatalf("unexpected cloned .gitone.json contents: %q", metadata)
	}

	updatedReadme := append(readme, []byte("Updated through Git Smart HTTP.\n")...)
	if err = os.WriteFile(filepath.Join(checkout, "README.md"), updatedReadme, 0644); err != nil {
		t.Fatal(err)
	}
	worktree, err := clonedRepository.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = worktree.Add("README.md"); err != nil {
		t.Fatal(err)
	}
	pushedCommit, err := worktree.Commit("Update README", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "alice",
			Email: "alice@localhost",
			When:  time.Now().UTC(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = clonedRepository.Push(&git.PushOptions{Auth: credentials}); err != nil {
		t.Fatalf("push repository: %v", err)
	}

	verificationCheckout := filepath.Join(t.TempDir(), "hello")
	verifiedRepository, err := git.PlainClone(verificationCheckout, false, &git.CloneOptions{
		URL:  server.URL + "/doh/whaat/hello.git",
		Auth: credentials,
	})
	if err != nil {
		t.Fatalf("clone pushed repository: %v", err)
	}
	verifiedHead, err := verifiedRepository.Head()
	if err != nil {
		t.Fatal(err)
	}
	if verifiedHead.Hash() != pushedCommit {
		t.Fatalf("expected pushed commit %s, got %s", pushedCommit, verifiedHead.Hash())
	}
	verifiedReadme, err := os.ReadFile(filepath.Join(verificationCheckout, "README.md"))
	if err != nil {
		t.Fatalf("pushed README.md does not exist: %v", err)
	}
	if string(verifiedReadme) != string(updatedReadme) {
		t.Fatalf("unexpected pushed README.md contents: %q", verifiedReadme)
	}
}

func TestRepositoryBrowserAPI(t *testing.T) {
	root := t.TempDir()
	store := storage.Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", "Engineering projects"); err != nil {
		t.Fatal(err)
	}
	repositoryPath := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	if err := store.CreateRepository(repositoryPath, storage.CreateRepositoryOptions{
		InitializeReadme: true,
		Author:           "alice",
		Description:      "Backend API",
	}); err != nil {
		t.Fatal(err)
	}

	checkout := filepath.Join(t.TempDir(), "api")
	repository, err := git.PlainClone(checkout, false, &git.CloneOptions{
		URL: filepath.Join(root, "engineering", "api.git"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(checkout, "docs"), 0750); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(checkout, "docs", "guide.txt"), []byte("Browse me\n"), 0640); err != nil {
		t.Fatal(err)
	}
	nodeSource := "const http = require(\"node:http\");\n\nhttp.createServer((_request, response) => response.end(\"ok\"));\n"
	if err = os.WriteFile(filepath.Join(checkout, "server.js"), []byte(nodeSource), 0640); err != nil {
		t.Fatal(err)
	}
	worktree, err := repository.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = worktree.Add("docs/guide.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err = worktree.Add("server.js"); err != nil {
		t.Fatal(err)
	}
	if _, err = worktree.Commit("Add guide", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "alice",
			Email: "alice@localhost",
			When:  time.Now().UTC(),
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err = repository.Push(&git.PushOptions{}); err != nil {
		t.Fatal(err)
	}
	head, err := repository.Head()
	if err != nil {
		t.Fatal(err)
	}

	handler := New(Config{
		Root:      root,
		Directory: testLDAPDirectory,
	})
	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.SetBasicAuth("alice", "secret")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s: expected status %d, got %d: %s", path, http.StatusOK, response.Code, response.Body.String())
		}
		return response
	}

	unauthenticated := httptest.NewRequest(http.MethodGet, "/api/repositories/engineering%2Fapi/tree/HEAD", nil)
	unauthenticatedResponse := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticatedResponse, unauthenticated)
	if unauthenticatedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("repository browser authentication: expected status %d, got %d", http.StatusUnauthorized, unauthenticatedResponse.Code)
	}

	createBranch := httptest.NewRequest(
		http.MethodPost,
		"/api/repositories/engineering%2Fapi/branches/feature%2Fdocs?from=main",
		nil,
	)
	createBranch.SetBasicAuth("alice", "secret")
	createBranchResponse := httptest.NewRecorder()
	handler.ServeHTTP(createBranchResponse, createBranch)
	if createBranchResponse.Code != http.StatusCreated {
		t.Fatalf("create branch: expected status %d, got %d: %s", http.StatusCreated, createBranchResponse.Code, createBranchResponse.Body.String())
	}
	var createdBranch struct {
		Repository string `json:"repository"`
		Name       string `json:"name"`
		From       string `json:"from"`
		Commit     string `json:"commit"`
	}
	if err = json.Unmarshal(createBranchResponse.Body.Bytes(), &createdBranch); err != nil {
		t.Fatal(err)
	}
	if createdBranch.Repository != "engineering/api" ||
		createdBranch.Name != "feature/docs" ||
		createdBranch.From != "main" ||
		createdBranch.Commit != head.Hash().String() {
		t.Fatalf("unexpected created branch: %#v", createdBranch)
	}

	duplicateBranch := httptest.NewRequest(
		http.MethodPost,
		"/api/repositories/engineering%2Fapi/branches/feature%2Fdocs?from=main",
		nil,
	)
	duplicateBranch.SetBasicAuth("alice", "secret")
	duplicateBranchResponse := httptest.NewRecorder()
	handler.ServeHTTP(duplicateBranchResponse, duplicateBranch)
	if duplicateBranchResponse.Code != http.StatusConflict {
		t.Fatalf("duplicate branch: expected status %d, got %d: %s", http.StatusConflict, duplicateBranchResponse.Code, duplicateBranchResponse.Body.String())
	}

	missingSource := httptest.NewRequest(
		http.MethodPost,
		"/api/repositories/engineering%2Fapi/branches/other?from=missing",
		nil,
	)
	missingSource.SetBasicAuth("alice", "secret")
	missingSourceResponse := httptest.NewRecorder()
	handler.ServeHTTP(missingSourceResponse, missingSource)
	if missingSourceResponse.Code != http.StatusNotFound {
		t.Fatalf("missing source branch: expected status %d, got %d: %s", http.StatusNotFound, missingSourceResponse.Code, missingSourceResponse.Body.String())
	}

	var branches struct {
		Repository    string `json:"repository"`
		DefaultBranch string `json:"defaultBranch"`
		Branches      []struct {
			Name   string `json:"name"`
			Commit string `json:"commit"`
		} `json:"branches"`
	}
	if err = json.Unmarshal(get("/api/repositories/engineering%2Fapi/branches").Body.Bytes(), &branches); err != nil {
		t.Fatal(err)
	}
	if branches.Repository != "engineering/api" ||
		branches.DefaultBranch != "main" ||
		len(branches.Branches) != 2 ||
		branches.Branches[0].Name != "feature/docs" ||
		branches.Branches[0].Commit != head.Hash().String() ||
		branches.Branches[1].Name != "main" {
		t.Fatalf("unexpected repository branches: %#v", branches)
	}

	var rootTree struct {
		Repository string `json:"repository"`
		Ref        string `json:"ref"`
		Commit     string `json:"commit"`
		Entries    []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"entries"`
	}
	if err = json.Unmarshal(get("/api/repositories/engineering%2Fapi/tree/main").Body.Bytes(), &rootTree); err != nil {
		t.Fatal(err)
	}
	if rootTree.Repository != "engineering/api" ||
		rootTree.Ref != "main" ||
		rootTree.Commit == "" ||
		len(rootTree.Entries) != 4 {
		t.Fatalf("unexpected repository root: %#v", rootTree)
	}
	var featureTree struct {
		Ref    string `json:"ref"`
		Commit string `json:"commit"`
	}
	if err = json.Unmarshal(get("/api/repositories/engineering%2Fapi/tree/feature%2Fdocs").Body.Bytes(), &featureTree); err != nil {
		t.Fatal(err)
	}
	if featureTree.Ref != "feature/docs" || featureTree.Commit != head.Hash().String() {
		t.Fatalf("unexpected feature branch tree: %#v", featureTree)
	}

	var directory struct {
		Path    string `json:"path"`
		Entries []struct {
			Name string `json:"name"`
			Type string `json:"type"`
			Size int64  `json:"size"`
		} `json:"entries"`
	}
	if err = json.Unmarshal(get("/api/repositories/engineering%2Fapi/tree/HEAD/docs").Body.Bytes(), &directory); err != nil {
		t.Fatal(err)
	}
	if directory.Path != "docs" ||
		len(directory.Entries) != 1 ||
		directory.Entries[0].Name != "guide.txt" ||
		directory.Entries[0].Type != "file" ||
		directory.Entries[0].Size != int64(len("Browse me\n")) {
		t.Fatalf("unexpected repository directory: %#v", directory)
	}

	var blob struct {
		Path     string `json:"path"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}
	if err = json.Unmarshal(get("/api/repositories/engineering%2Fapi/blob/HEAD/docs%2Fguide.txt").Body.Bytes(), &blob); err != nil {
		t.Fatal(err)
	}
	if blob.Path != "docs/guide.txt" || blob.Encoding != "utf-8" || blob.Content != "Browse me\n" {
		t.Fatalf("unexpected repository blob: %#v", blob)
	}
	var highlightedBlob struct {
		Path            string `json:"path"`
		Encoding        string `json:"encoding"`
		Content         string `json:"content"`
		Language        string `json:"language"`
		HighlightedHTML string `json:"highlightedHtml"`
		CanEdit         bool   `json:"canEdit"`
	}
	if err = json.Unmarshal(get("/api/repositories/engineering%2Fapi/blob/HEAD/server.js").Body.Bytes(), &highlightedBlob); err != nil {
		t.Fatal(err)
	}
	if highlightedBlob.Path != "server.js" ||
		highlightedBlob.Encoding != "utf-8" ||
		highlightedBlob.Content != nodeSource ||
		highlightedBlob.Language != "JavaScript" ||
		!strings.Contains(highlightedBlob.HighlightedHTML, "<span") ||
		!strings.Contains(highlightedBlob.HighlightedHTML, "node:http") ||
		highlightedBlob.CanEdit {
		t.Fatalf("unexpected highlighted repository blob: %#v", highlightedBlob)
	}

	headCommit, err := repository.CommitObject(head.Hash())
	if err != nil {
		t.Fatal(err)
	}
	initialCommit, err := headCommit.Parent(0)
	if err != nil {
		t.Fatal(err)
	}
	var commitDiff struct {
		Repository string `json:"repository"`
		Commit     string `json:"commit"`
		Parent     string `json:"parent"`
		Files      []struct {
			Path      string `json:"path"`
			Status    string `json:"status"`
			Additions int    `json:"additions"`
			Patch     string `json:"patch"`
		} `json:"files"`
	}
	if err = json.Unmarshal(get(
		"/api/repositories/engineering%2Fapi/commits/"+head.Hash().String()+"/diff",
	).Body.Bytes(), &commitDiff); err != nil {
		t.Fatal(err)
	}
	if commitDiff.Repository != "engineering/api" ||
		commitDiff.Commit != head.Hash().String() ||
		commitDiff.Parent != initialCommit.Hash.String() ||
		len(commitDiff.Files) != 2 {
		t.Fatalf("unexpected commit diff: %#v", commitDiff)
	}
	commitDiffFiles := map[string]struct {
		Status    string
		Additions int
		Patch     string
	}{}
	for _, file := range commitDiff.Files {
		commitDiffFiles[file.Path] = struct {
			Status    string
			Additions int
			Patch     string
		}{file.Status, file.Additions, file.Patch}
	}
	if commitDiffFiles["docs/guide.txt"].Status != "added" ||
		commitDiffFiles["docs/guide.txt"].Additions != 1 ||
		!strings.Contains(commitDiffFiles["docs/guide.txt"].Patch, "Browse me") ||
		commitDiffFiles["server.js"].Status != "added" ||
		!strings.Contains(commitDiffFiles["server.js"].Patch, "node:http") {
		t.Fatalf("unexpected files in commit diff: %#v", commitDiffFiles)
	}

	var initialDiff struct {
		Commit string `json:"commit"`
		Parent string `json:"parent"`
		Files  []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	if err = json.Unmarshal(get(
		"/api/repositories/engineering%2Fapi/commits/"+initialCommit.Hash.String()+"/diff",
	).Body.Bytes(), &initialDiff); err != nil {
		t.Fatal(err)
	}
	if initialDiff.Commit != initialCommit.Hash.String() ||
		initialDiff.Parent != "" ||
		len(initialDiff.Files) != 2 {
		t.Fatalf("unexpected initial commit diff: %#v", initialDiff)
	}

	var editableBlob struct {
		Commit  string `json:"commit"`
		CanEdit bool   `json:"canEdit"`
	}
	if err = json.Unmarshal(get("/api/repositories/engineering%2Fapi/blob/main/server.js").Body.Bytes(), &editableBlob); err != nil {
		t.Fatal(err)
	}
	if editableBlob.Commit != head.Hash().String() || !editableBlob.CanEdit {
		t.Fatalf("expected main branch blob to be editable: %#v", editableBlob)
	}

	updatedGuide := "Edited through the repository browser\n"
	updateBody, err := json.Marshal(map[string]string{
		"content":        updatedGuide,
		"message":        "Update browsing guide",
		"expectedCommit": head.Hash().String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	updateRequest := httptest.NewRequest(
		http.MethodPut,
		"/api/repositories/engineering%2Fapi/files/main/docs%2Fguide.txt",
		bytes.NewReader(updateBody),
	)
	updateRequest.Header.Set("Content-Type", "application/json")
	updateRequest.SetBasicAuth("alice", "secret")
	updateResponse := httptest.NewRecorder()
	handler.ServeHTTP(updateResponse, updateRequest)
	if updateResponse.Code != http.StatusOK {
		t.Fatalf("edit file: expected status %d, got %d: %s", http.StatusOK, updateResponse.Code, updateResponse.Body.String())
	}
	var updatedFile struct {
		Repository     string `json:"repository"`
		Branch         string `json:"branch"`
		Path           string `json:"path"`
		Commit         string `json:"commit"`
		PreviousCommit string `json:"previousCommit"`
		Message        string `json:"message"`
	}
	if err = json.Unmarshal(updateResponse.Body.Bytes(), &updatedFile); err != nil {
		t.Fatal(err)
	}
	if updatedFile.Repository != "engineering/api" ||
		updatedFile.Branch != "main" ||
		updatedFile.Path != "docs/guide.txt" ||
		updatedFile.Commit == "" ||
		updatedFile.Commit == head.Hash().String() ||
		updatedFile.PreviousCommit != head.Hash().String() ||
		updatedFile.Message != "Update browsing guide" {
		t.Fatalf("unexpected edited file response: %#v", updatedFile)
	}

	barePath, err := store.GitPath(repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	bareRepository, err := git.PlainOpen(barePath)
	if err != nil {
		t.Fatal(err)
	}
	mainReference, err := bareRepository.Reference(plumbing.NewBranchReferenceName("main"), false)
	if err != nil {
		t.Fatal(err)
	}
	if mainReference.Hash().String() != updatedFile.Commit {
		t.Fatalf("expected main at %s, got %s", updatedFile.Commit, mainReference.Hash())
	}
	editedCommit, err := bareRepository.CommitObject(mainReference.Hash())
	if err != nil {
		t.Fatal(err)
	}
	if len(editedCommit.ParentHashes) != 1 ||
		editedCommit.ParentHashes[0] != head.Hash() ||
		editedCommit.Message != "Update browsing guide\n" {
		t.Fatalf("unexpected edited commit: %#v", editedCommit)
	}
	editedGuide, err := editedCommit.File("docs/guide.txt")
	if err != nil {
		t.Fatal(err)
	}
	editedContents, err := editedGuide.Contents()
	if err != nil {
		t.Fatal(err)
	}
	if editedContents != updatedGuide || editedGuide.Mode != filemode.Regular {
		t.Fatalf("unexpected edited guide: mode %s, contents %q", editedGuide.Mode, editedContents)
	}
	unchangedServer, err := editedCommit.File("server.js")
	if err != nil {
		t.Fatal(err)
	}
	unchangedServerContents, err := unchangedServer.Contents()
	if err != nil {
		t.Fatal(err)
	}
	if unchangedServerContents != nodeSource {
		t.Fatalf("editing docs/guide.txt changed server.js: %q", unchangedServerContents)
	}
	featureReference, err := bareRepository.Reference(plumbing.NewBranchReferenceName("feature/docs"), false)
	if err != nil {
		t.Fatal(err)
	}
	if featureReference.Hash() != head.Hash() {
		t.Fatalf("editing main moved feature/docs to %s", featureReference.Hash())
	}

	staleRequest := httptest.NewRequest(
		http.MethodPut,
		"/api/repositories/engineering%2Fapi/files/main/docs%2Fguide.txt",
		bytes.NewReader(updateBody),
	)
	staleRequest.Header.Set("Content-Type", "application/json")
	staleRequest.SetBasicAuth("alice", "secret")
	staleResponse := httptest.NewRecorder()
	handler.ServeHTTP(staleResponse, staleRequest)
	if staleResponse.Code != http.StatusConflict {
		t.Fatalf("stale edit: expected status %d, got %d: %s", http.StatusConflict, staleResponse.Code, staleResponse.Body.String())
	}
	currentMain, err := bareRepository.Reference(plumbing.NewBranchReferenceName("main"), false)
	if err != nil {
		t.Fatal(err)
	}
	if currentMain.Hash().String() != updatedFile.Commit {
		t.Fatalf("stale edit moved main to %s", currentMain.Hash())
	}

	var savedBlob struct {
		Commit  string `json:"commit"`
		Content string `json:"content"`
		CanEdit bool   `json:"canEdit"`
	}
	if err = json.Unmarshal(get("/api/repositories/engineering%2Fapi/blob/main/docs%2Fguide.txt").Body.Bytes(), &savedBlob); err != nil {
		t.Fatal(err)
	}
	if savedBlob.Commit != updatedFile.Commit ||
		savedBlob.Content != updatedGuide ||
		!savedBlob.CanEdit {
		t.Fatalf("unexpected saved repository blob: %#v", savedBlob)
	}

	var commits struct {
		Ref     string `json:"ref"`
		Total   int    `json:"total"`
		Commits []struct {
			Message string `json:"message"`
		} `json:"commits"`
	}
	if err = json.Unmarshal(get("/api/repositories/engineering%2Fapi/commits/feature%2Fdocs?limit=100").Body.Bytes(), &commits); err != nil {
		t.Fatal(err)
	}
	if commits.Ref != "feature/docs" ||
		commits.Total != 2 ||
		len(commits.Commits) != 2 ||
		commits.Commits[0].Message != "Add guide" {
		t.Fatalf("unexpected repository commits: %#v", commits)
	}
}

func TestRepositoryBrowserResolvesLFSContent(t *testing.T) {
	const (
		oid     = "f7895326610712feb431767ef21f7e7eaec2bee6d99db789a212ed3a872b8f2a"
		content = "hello from inside LFS\n"
		pointer = "version https://git-lfs.github.com/spec/v1\n" +
			"oid sha256:" + oid + "\n" +
			"size 22\n"
	)

	root := t.TempDir()
	store := storage.Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	repositoryPath := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	if err := store.CreateRepository(repositoryPath, storage.CreateRepositoryOptions{
		InitializeReadme: true,
		Author:           "alice",
	}); err != nil {
		t.Fatal(err)
	}

	lfsPath, err := store.LFSPath(repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	objectPath := filepath.Join(lfsPath, "objects", oid[:2], oid[2:4], oid)
	if err = os.MkdirAll(filepath.Dir(objectPath), 0750); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(objectPath, []byte(content), 0640); err != nil {
		t.Fatal(err)
	}

	gitPath, err := store.GitPath(repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(t.TempDir(), "api")
	repository, err := git.PlainClone(checkout, false, &git.CloneOptions{URL: gitPath})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(checkout, "notes.txt"), []byte(pointer), 0644); err != nil {
		t.Fatal(err)
	}
	worktree, err := repository.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = worktree.Add("notes.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err = worktree.Commit("Add LFS notes", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "alice",
			Email: "alice@localhost",
			When:  time.Now().UTC(),
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err = repository.Push(&git.PushOptions{}); err != nil {
		t.Fatal(err)
	}

	handler := New(Config{
		Root:      root,
		Directory: testLDAPDirectory,
	})
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/repositories/engineering%2Fapi/blob/main/notes.txt",
		nil,
	)
	request.SetBasicAuth("alice", "secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("read LFS file: expected status %d, got %d: %s", http.StatusOK, response.Code, response.Body.String())
	}
	var blob struct {
		Size     int64  `json:"size"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
		CanEdit  bool   `json:"canEdit"`
		LFS      bool   `json:"lfs"`
		LFSOID   string `json:"lfsOid"`
	}
	if err = json.Unmarshal(response.Body.Bytes(), &blob); err != nil {
		t.Fatal(err)
	}
	if blob.Size != int64(len(content)) ||
		blob.Encoding != "utf-8" ||
		blob.Content != content ||
		blob.CanEdit ||
		!blob.LFS ||
		blob.LFSOID != oid {
		t.Fatalf("unexpected resolved LFS blob: %#v", blob)
	}

	treeRequest := httptest.NewRequest(
		http.MethodGet,
		"/api/repositories/engineering%2Fapi/tree/main",
		nil,
	)
	treeRequest.SetBasicAuth("alice", "secret")
	treeResponse := httptest.NewRecorder()
	handler.ServeHTTP(treeResponse, treeRequest)
	if treeResponse.Code != http.StatusOK {
		t.Fatalf("list LFS file: expected status %d, got %d: %s", http.StatusOK, treeResponse.Code, treeResponse.Body.String())
	}
	var tree struct {
		Entries []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
			LFS  bool   `json:"lfs"`
		} `json:"entries"`
	}
	if err = json.Unmarshal(treeResponse.Body.Bytes(), &tree); err != nil {
		t.Fatal(err)
	}
	var lfsEntry *struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
		LFS  bool   `json:"lfs"`
	}
	for index := range tree.Entries {
		if tree.Entries[index].Name == "notes.txt" {
			lfsEntry = &tree.Entries[index]
			break
		}
	}
	if lfsEntry == nil || !lfsEntry.LFS || lfsEntry.Size != int64(len(content)) {
		t.Fatalf("unexpected LFS tree entry: %#v", lfsEntry)
	}
}

func TestCompareAndMergeRepositoryBranches(t *testing.T) {
	root := t.TempDir()
	store := storage.Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", "Engineering projects"); err != nil {
		t.Fatal(err)
	}
	repositoryPath := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	if err := store.CreateRepository(repositoryPath, storage.CreateRepositoryOptions{
		InitializeReadme: true,
		Author:           "alice",
		Description:      "Backend API",
	}); err != nil {
		t.Fatal(err)
	}
	barePath, err := store.GitPath(repositoryPath)
	if err != nil {
		t.Fatal(err)
	}

	featureCommit := commitBranchFile(
		t,
		barePath,
		"feature",
		"feature.txt",
		"Feature branch\n",
		"Add feature",
	)
	mainCommit := commitBranchFile(
		t,
		barePath,
		"main",
		"main.txt",
		"Main branch\n",
		"Update main",
	)

	handler := New(Config{
		Root:      root,
		Directory: testLDAPDirectory,
	})
	do := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.SetBasicAuth("alice", "secret")
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	comparisonResponse := do(
		http.MethodGet,
		"/api/repositories/engineering%2Fapi/compare?base=main&head=feature",
		"",
	)
	if comparisonResponse.Code != http.StatusOK {
		t.Fatalf("compare branches: expected status %d, got %d: %s", http.StatusOK, comparisonResponse.Code, comparisonResponse.Body.String())
	}
	var comparison struct {
		Base       string `json:"base"`
		Head       string `json:"head"`
		BaseCommit string `json:"baseCommit"`
		HeadCommit string `json:"headCommit"`
		Ahead      int    `json:"ahead"`
		Behind     int    `json:"behind"`
		Mergeable  bool   `json:"mergeable"`
		CanMerge   bool   `json:"canMerge"`
		Conflicts  []string
		Files      []struct {
			Path      string `json:"path"`
			Status    string `json:"status"`
			Additions int    `json:"additions"`
			Patch     string `json:"patch"`
		} `json:"files"`
	}
	if err = json.Unmarshal(comparisonResponse.Body.Bytes(), &comparison); err != nil {
		t.Fatal(err)
	}
	if comparison.Base != "main" ||
		comparison.Head != "feature" ||
		comparison.BaseCommit != mainCommit.String() ||
		comparison.HeadCommit != featureCommit.String() ||
		comparison.Ahead != 1 ||
		comparison.Behind != 1 ||
		!comparison.Mergeable ||
		!comparison.CanMerge ||
		len(comparison.Conflicts) != 0 ||
		len(comparison.Files) != 1 ||
		comparison.Files[0].Path != "feature.txt" ||
		comparison.Files[0].Status != "added" ||
		comparison.Files[0].Additions != 1 ||
		!strings.Contains(comparison.Files[0].Patch, "+Feature branch") {
		t.Fatalf("unexpected branch comparison: %#v", comparison)
	}

	mergeResponse := do(
		http.MethodPost,
		"/api/repositories/engineering%2Fapi/merges",
		`{"target":"main","source":"feature"}`,
	)
	if mergeResponse.Code != http.StatusOK {
		t.Fatalf("merge branches: expected status %d, got %d: %s", http.StatusOK, mergeResponse.Code, mergeResponse.Body.String())
	}
	var merged struct {
		Commit   string `json:"commit"`
		Strategy string `json:"strategy"`
	}
	if err = json.Unmarshal(mergeResponse.Body.Bytes(), &merged); err != nil {
		t.Fatal(err)
	}
	if merged.Commit == "" || merged.Strategy != "merge-commit" {
		t.Fatalf("unexpected merge result: %#v", merged)
	}
	bareRepository, err := git.PlainOpen(barePath)
	if err != nil {
		t.Fatal(err)
	}
	mainReference, err := bareRepository.Reference(plumbing.NewBranchReferenceName("main"), false)
	if err != nil {
		t.Fatal(err)
	}
	if mainReference.Hash().String() != merged.Commit {
		t.Fatalf("main points to %s instead of merge commit %s", mainReference.Hash(), merged.Commit)
	}
	mergeCommit, err := bareRepository.CommitObject(mainReference.Hash())
	if err != nil {
		t.Fatal(err)
	}
	if len(mergeCommit.ParentHashes) != 2 ||
		mergeCommit.ParentHashes[0] != mainCommit ||
		mergeCommit.ParentHashes[1] != featureCommit {
		t.Fatalf("unexpected merge parents: %#v", mergeCommit.ParentHashes)
	}
	if _, err = mergeCommit.File("main.txt"); err != nil {
		t.Fatalf("merged tree is missing main.txt: %v", err)
	}
	if _, err = mergeCommit.File("feature.txt"); err != nil {
		t.Fatalf("merged tree is missing feature.txt: %v", err)
	}

	conflictTarget := commitBranchFile(
		t,
		barePath,
		"conflict-target",
		"README.md",
		"Target version\n",
		"Edit README on target",
	)
	_ = commitBranchFile(
		t,
		barePath,
		"conflict-source",
		"README.md",
		"Source version\n",
		"Edit README on source",
	)
	conflictComparisonResponse := do(
		http.MethodGet,
		"/api/repositories/engineering%2Fapi/compare?base=conflict-target&head=conflict-source",
		"",
	)
	if conflictComparisonResponse.Code != http.StatusOK {
		t.Fatalf("compare conflicting branches: expected status %d, got %d: %s", http.StatusOK, conflictComparisonResponse.Code, conflictComparisonResponse.Body.String())
	}
	var conflictComparison struct {
		Mergeable bool     `json:"mergeable"`
		Conflicts []string `json:"conflicts"`
	}
	if err = json.Unmarshal(conflictComparisonResponse.Body.Bytes(), &conflictComparison); err != nil {
		t.Fatal(err)
	}
	if conflictComparison.Mergeable ||
		len(conflictComparison.Conflicts) != 1 ||
		conflictComparison.Conflicts[0] != "README.md" {
		t.Fatalf("unexpected conflict assessment: %#v", conflictComparison)
	}

	conflictMergeResponse := do(
		http.MethodPost,
		"/api/repositories/engineering%2Fapi/merges",
		`{"target":"conflict-target","source":"conflict-source"}`,
	)
	if conflictMergeResponse.Code != http.StatusConflict {
		t.Fatalf("conflicting merge: expected status %d, got %d: %s", http.StatusConflict, conflictMergeResponse.Code, conflictMergeResponse.Body.String())
	}
	conflictTargetReference, err := bareRepository.Reference(
		plumbing.NewBranchReferenceName("conflict-target"),
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if conflictTargetReference.Hash() != conflictTarget {
		t.Fatalf("conflicting merge moved target from %s to %s", conflictTarget, conflictTargetReference.Hash())
	}
}

func commitBranchFile(
	t *testing.T,
	remotePath string,
	branch string,
	fileName string,
	content string,
	message string,
) plumbing.Hash {
	t.Helper()
	checkout := filepath.Join(t.TempDir(), branch)
	repository, err := git.PlainClone(checkout, false, &git.CloneOptions{URL: remotePath})
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := repository.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	branchReference := plumbing.NewBranchReferenceName(branch)
	if branch != "main" {
		if err = worktree.Checkout(&git.CheckoutOptions{
			Branch: branchReference,
			Create: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.WriteFile(filepath.Join(checkout, fileName), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = worktree.Add(fileName); err != nil {
		t.Fatal(err)
	}
	hash, err := worktree.Commit(message, &git.CommitOptions{
		Author: &object.Signature{
			Name:  "alice",
			Email: "alice@localhost",
			When:  time.Now().UTC(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	refSpec := config.RefSpec(
		branchReference.String() + ":" + branchReference.String(),
	)
	if err = repository.Push(&git.PushOptions{RefSpecs: []config.RefSpec{refSpec}}); err != nil {
		t.Fatal(err)
	}
	return hash
}

func TestHumaGroupNavigationAPI(t *testing.T) {
	root := t.TempDir()
	store := storage.Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", "Product engineering"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateGroup("engineering/backend", "alice", "Backend services"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRepository(repopath.Repository{Groups: []string{"engineering"}, Name: "web"}, storage.CreateRepositoryOptions{
		Description: "Engineering web portal",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRepository(repopath.Repository{Groups: []string{"engineering", "backend"}, Name: "api"}, storage.CreateRepositoryOptions{
		Description: "Backend API",
	}); err != nil {
		t.Fatal(err)
	}
	handler := New(Config{
		Root:      root,
		Directory: testLDAPDirectory,
	})

	listRequest := httptest.NewRequest(http.MethodGet, "/api/groups", nil)
	listRequest.SetBasicAuth("alice", "secret")
	listResponse := httptest.NewRecorder()
	handler.ServeHTTP(listResponse, listRequest)
	if listResponse.Code != http.StatusOK {
		t.Fatalf("list groups: expected status %d, got %d: %s", http.StatusOK, listResponse.Code, listResponse.Body.String())
	}
	var list struct {
		Groups []struct {
			Name        string `json:"name"`
			Path        string `json:"path"`
			Description string `json:"description"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(listResponse.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Groups) != 1 ||
		list.Groups[0].Path != "engineering" ||
		list.Groups[0].Description != "Product engineering" {
		t.Fatalf("unexpected top-level groups: %#v", list.Groups)
	}

	parentRequest := httptest.NewRequest(http.MethodGet, "/api/groups/engineering", nil)
	parentRequest.SetBasicAuth("alice", "secret")
	parentResponse := httptest.NewRecorder()
	handler.ServeHTTP(parentResponse, parentRequest)
	if parentResponse.Code != http.StatusOK {
		t.Fatalf("get parent group: expected status %d, got %d: %s", http.StatusOK, parentResponse.Code, parentResponse.Body.String())
	}
	var parent struct {
		Description string `json:"description"`
		Username    string `json:"username"`
		Subgroups   []struct {
			Path        string `json:"path"`
			Description string `json:"description"`
		} `json:"subgroups"`
		Repositories []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal(parentResponse.Body.Bytes(), &parent); err != nil {
		t.Fatal(err)
	}
	if len(parent.Subgroups) != 1 ||
		parent.Description != "Product engineering" ||
		parent.Username != "alice" ||
		parent.Subgroups[0].Path != "engineering/backend" ||
		parent.Subgroups[0].Description != "Backend services" ||
		len(parent.Repositories) != 1 ||
		parent.Repositories[0].Name != "web" ||
		parent.Repositories[0].Description != "Engineering web portal" {
		t.Fatalf("unexpected parent group detail: %#v", parent)
	}

	detailRequest := httptest.NewRequest(http.MethodGet, "/api/groups/engineering%2Fbackend", nil)
	detailRequest.SetBasicAuth("alice", "secret")
	detailResponse := httptest.NewRecorder()
	handler.ServeHTTP(detailResponse, detailRequest)
	if detailResponse.Code != http.StatusOK {
		t.Fatalf("get group: expected status %d, got %d: %s", http.StatusOK, detailResponse.Code, detailResponse.Body.String())
	}
	var detail struct {
		Path         string `json:"path"`
		Description  string `json:"description"`
		Username     string `json:"username"`
		Repositories []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal(detailResponse.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Path != "engineering/backend" ||
		detail.Description != "Backend services" ||
		detail.Username != "alice" ||
		len(detail.Repositories) != 1 ||
		detail.Repositories[0].Name != "api" ||
		detail.Repositories[0].Description != "Backend API" {
		t.Fatalf("unexpected group detail: %#v", detail)
	}

	deleteNonEmptyGroup := httptest.NewRequest(http.MethodDelete, "/api/groups/engineering%2Fbackend", nil)
	deleteNonEmptyGroup.SetBasicAuth("alice", "secret")
	deleteNonEmptyGroupResponse := httptest.NewRecorder()
	handler.ServeHTTP(deleteNonEmptyGroupResponse, deleteNonEmptyGroup)
	if deleteNonEmptyGroupResponse.Code != http.StatusConflict {
		t.Fatalf("delete non-empty group: expected status %d, got %d: %s", http.StatusConflict, deleteNonEmptyGroupResponse.Code, deleteNonEmptyGroupResponse.Body.String())
	}
}

func TestTypeScriptUIAndHumaDocs(t *testing.T) {
	handler := New(Config{
		Root:      t.TempDir(),
		Directory: testLDAPDirectory,
	})

	unauthenticated := httptest.NewRequest(http.MethodGet, "/", nil)
	unauthenticatedResponse := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticatedResponse, unauthenticated)
	if unauthenticatedResponse.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, unauthenticatedResponse.Code)
	}
	if unauthenticatedResponse.Header().Get("WWW-Authenticate") != "" {
		t.Fatal("UI shell should not trigger a browser Basic Auth challenge")
	}

	for _, path := range []string{"/", "/groups/engineering/backend", "/repositories/engineering/api"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.SetBasicAuth("alice", "secret")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s: expected status %d, got %d", path, http.StatusOK, response.Code)
		}
		body := response.Body.String()
		if !strings.Contains(body, `<main id="app"`) ||
			!strings.Contains(body, `<img src="/assets/gitone.png" alt="GitOne">`) ||
			!strings.Contains(body, `<script src="/assets/diff.min.js"></script>`) ||
			!strings.Contains(body, `<script type="module" src="/assets/app.js?v=18">`) ||
			!strings.Contains(body, `"marked": "/assets/marked.esm.js"`) ||
			!strings.Contains(body, `localStorage.getItem("gitone-color-theme")`) ||
			!strings.Contains(body, `<select id="color-theme" aria-label="Color theme">`) ||
			!strings.Contains(body, `<option value="dark" selected>Dark</option>`) ||
			!strings.Contains(body, `<option value="github">GitHub</option>`) ||
			!strings.Contains(body, `<option value="gitlab">GitLab</option>`) ||
			!strings.Contains(body, `<div id="session-controls"`) ||
			!strings.Contains(body, `<button id="logout"`) ||
			!strings.Contains(body, `<div id="notifications"`) {
			t.Fatalf("%s did not serve the TypeScript UI shell", path)
		}
		if strings.Contains(body, `<h1><a href="/">GitOne</a></h1>`) ||
			strings.Contains(body, `A small Git and Git LFS server.`) {
			t.Fatalf("%s served removed header text", path)
		}
	}

	iconRequest := httptest.NewRequest(http.MethodGet, "/assets/gitone.png", nil)
	iconRequest.SetBasicAuth("alice", "secret")
	iconResponse := httptest.NewRecorder()
	handler.ServeHTTP(iconResponse, iconRequest)
	if iconResponse.Code != http.StatusOK {
		t.Fatalf("GitOne icon was not served: %d", iconResponse.Code)
	}
	if iconResponse.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("unexpected GitOne icon content type: %q", iconResponse.Header().Get("Content-Type"))
	}
	if !strings.HasPrefix(iconResponse.Body.String(), "\x89PNG\r\n\x1a\n") {
		t.Fatal("served GitOne icon is not a PNG")
	}

	assetRequest := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	assetRequest.SetBasicAuth("alice", "secret")
	assetResponse := httptest.NewRecorder()
	handler.ServeHTTP(assetResponse, assetRequest)
	if assetResponse.Code != http.StatusOK || !strings.Contains(assetResponse.Body.String(), "renderGroup") {
		t.Fatalf("TypeScript asset was not served: %d", assetResponse.Code)
	}
	if assetResponse.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("unexpected asset cache policy: %q", assetResponse.Header().Get("Cache-Control"))
	}
	if !strings.Contains(assetResponse.Body.String(), "initializeColorTheme") ||
		!strings.Contains(assetResponse.Body.String(), "localStorage.setItem(colorThemeStorageKey, theme)") ||
		!strings.Contains(assetResponse.Body.String(), "document.documentElement.dataset.theme = theme") ||
		!strings.Contains(assetResponse.Body.String(), "renderLogin") ||
		!strings.Contains(assetResponse.Body.String(), `request("/api/session"`) {
		t.Fatal("served UI does not persist and apply color themes")
	}
	if strings.Contains(assetResponse.Body.String(), `"Recent commits"`) {
		t.Fatal("served UI should reserve commit lists for history")
	}
	if strings.Contains(assetResponse.Body.String(), `sectionHeading(content.path || "Files"`) {
		t.Fatal("served UI should not repeat the files view title above the repository tree")
	}
	stylesRequest := httptest.NewRequest(http.MethodGet, "/assets/styles.css", nil)
	stylesRequest.SetBasicAuth("alice", "secret")
	stylesResponse := httptest.NewRecorder()
	handler.ServeHTTP(stylesResponse, stylesRequest)
	if stylesResponse.Code != http.StatusOK {
		t.Fatalf("theme styles were not served: %d", stylesResponse.Code)
	}
	for _, theme := range []string{
		"light",
		"steampunk",
		"windows",
		"macosx",
		"ubuntu",
		"solaris",
		"github",
		"gitlab",
	} {
		if !strings.Contains(stylesResponse.Body.String(), `:root[data-theme="`+theme+`"]`) {
			t.Fatalf("theme styles do not contain %q", theme)
		}
	}
	diffAssetRequest := httptest.NewRequest(http.MethodGet, "/assets/diff.min.js", nil)
	diffAssetRequest.SetBasicAuth("alice", "secret")
	diffAssetResponse := httptest.NewRecorder()
	handler.ServeHTTP(diffAssetResponse, diffAssetRequest)
	if diffAssetResponse.Code != http.StatusOK ||
		!strings.Contains(diffAssetResponse.Body.String(), "structuredPatch") {
		t.Fatalf("browser diff asset was not served: %d", diffAssetResponse.Code)
	}
	if !strings.Contains(assetResponse.Body.String(), "`${apiGroupURL(name)}?description=${encodeURIComponent(description.input.value)}`") {
		t.Fatal("served UI does not use the path-based group creation endpoint")
	}
	if !strings.Contains(assetResponse.Body.String(), "navigator.clipboard") ||
		!strings.Contains(assetResponse.Body.String(), "repositoryURL(groupPath, repositoryName, group.username)") {
		t.Fatal("served UI does not provide copyable full repository URLs")
	}
	if !strings.Contains(assetResponse.Body.String(), "DOMPurify.sanitize") ||
		!strings.Contains(assetResponse.Body.String(), "marked.parse") {
		t.Fatal("served UI does not safely render Markdown files")
	}
	if !strings.Contains(assetResponse.Body.String(), "content.highlightedHtml") ||
		!strings.Contains(assetResponse.Body.String(), `ALLOWED_TAGS: ["pre", "code", "span"]`) ||
		!strings.Contains(assetResponse.Body.String(), "highlighted-source") {
		t.Fatal("served UI does not safely render Chroma-highlighted source files")
	}
	if !strings.Contains(assetResponse.Body.String(), "repositoryFileAPIURL") ||
		!strings.Contains(assetResponse.Body.String(), "content.canEdit") ||
		!strings.Contains(assetResponse.Body.String(), "expectedCommit: content.commit") ||
		!strings.Contains(assetResponse.Body.String(), "window.Diff.structuredPatch") ||
		!strings.Contains(assetResponse.Body.String(), "No changes to commit.") ||
		!strings.Contains(assetResponse.Body.String(), "Commit changes") {
		t.Fatal("served UI does not support reviewing, editing, and committing repository files")
	}
	if !strings.Contains(assetResponse.Body.String(), "initializeReadme.checked = true") {
		t.Fatal("served UI does not default the README initialization option to checked")
	}
	if !strings.Contains(assetResponse.Body.String(), "repositoryDescription.input.value") {
		t.Fatal("served UI does not provide the repository description option")
	}
	if !strings.Contains(assetResponse.Body.String(), "repository.description") {
		t.Fatal("served UI does not show repository descriptions")
	}
	if !strings.Contains(assetResponse.Body.String(), "repositoryBrowserURL") ||
		!strings.Contains(assetResponse.Body.String(), "renderRepositoryBrowser") ||
		!strings.Contains(assetResponse.Body.String(), "repositoryBranchesAPIURL") ||
		!strings.Contains(assetResponse.Body.String(), `parameters.get("ref") || "main"`) ||
		!strings.Contains(assetResponse.Body.String(), `branchSelect.addEventListener("change"`) ||
		!strings.Contains(assetResponse.Body.String(), "repositoryBranchCreator") ||
		!strings.Contains(assetResponse.Body.String(), "repositoryBranchAPIURL") ||
		!strings.Contains(assetResponse.Body.String(), `?from=${encodeURIComponent(source.value)}`) ||
		!strings.Contains(assetResponse.Body.String(), "repositoryBranchComparison") ||
		!strings.Contains(assetResponse.Body.String(), "repositoryComparisonAPIURL") ||
		!strings.Contains(assetResponse.Body.String(), "repositoryMergesAPIURL") ||
		!strings.Contains(assetResponse.Body.String(), "branchComparisonResult") ||
		!strings.Contains(assetResponse.Body.String(), "repositoryHistory") ||
		!strings.Contains(assetResponse.Body.String(), "repositoryCommitDiffAPIURL") ||
		!strings.Contains(assetResponse.Body.String(), "Loading commit diff") ||
		!strings.Contains(assetResponse.Body.String(), "repositoryNavigation") ||
		!strings.Contains(assetResponse.Body.String(), `route.view === "history" ? 100 : 1`) ||
		!strings.Contains(assetResponse.Body.String(), `repositoryAPIURL(route.repository, "tree"`) ||
		!strings.Contains(assetResponse.Body.String(), `repositoryAPIURL(route.repository, "blob"`) {
		t.Fatal("served UI does not support repository files, branches, and commit history diffs")
	}
	if !strings.Contains(assetResponse.Body.String(), `actionButton("Clone", "copy", "primary")`) ||
		!strings.Contains(assetResponse.Body.String(), `"action-dialog clone-dialog"`) ||
		!strings.Contains(assetResponse.Body.String(), "const command = `git clone ${value}`") ||
		!strings.Contains(assetResponse.Body.String(), "copyButton(command)") ||
		!strings.Contains(assetResponse.Body.String(), "clone.trigger") ||
		!strings.Contains(assetResponse.Body.String(), "clone.dialog") {
		t.Fatal("served UI does not present the clone command from the repository toolbar")
	}
	if !strings.Contains(assetResponse.Body.String(), "branchPicker.append(branchLabel, branchCreator.trigger, branchComparison.trigger)") {
		t.Fatal("served UI does not place the new branch and compare actions beside the branch selector")
	}
	if !strings.Contains(assetResponse.Body.String(), "repositoryDeleteControl(data.path, repository.name)") ||
		!strings.Contains(assetResponse.Body.String(), `input.value !== repositoryName`) ||
		!strings.Contains(assetResponse.Body.String(), `method: "DELETE"`) {
		t.Fatal("served UI does not require repository-name confirmation before deletion")
	}
	if !strings.Contains(assetResponse.Body.String(), "data.subgroups.length === 0 && data.repositories.length === 0") ||
		!strings.Contains(assetResponse.Body.String(), `input.value !== groupPath`) ||
		!strings.Contains(assetResponse.Body.String(), `request(apiGroupURL(groupPath), { method: "DELETE" })`) {
		t.Fatal("served UI does not restrict group deletion to confirmed empty groups")
	}
	if !strings.Contains(assetResponse.Body.String(), "description.input.value") ||
		!strings.Contains(assetResponse.Body.String(), "subgroupDescription.input.value") {
		t.Fatal("served UI does not provide group and subgroup descriptions")
	}
	if !strings.Contains(assetResponse.Body.String(), "groupSettingsControl") ||
		!strings.Contains(assetResponse.Body.String(), "groupSettingsAPIURL") ||
		!strings.Contains(assetResponse.Body.String(), "newSecret: secret") ||
		!strings.Contains(assetResponse.Body.String(), `method: "PUT"`) {
		t.Fatal("served UI does not provide complete group control settings")
	}
	if strings.Contains(assetResponse.Body.String(), "memberSecrets") ||
		strings.Contains(assetResponse.Body.String(), "member-secret") {
		t.Fatal("served UI still exposes local member password controls")
	}
	if !strings.Contains(assetResponse.Body.String(), "group.description") {
		t.Fatal("served UI does not show group descriptions")
	}
	if !strings.Contains(assetResponse.Body.String(), "data.description") {
		t.Fatal("served UI does not show the selected group description")
	}

	for _, path := range []string{"/docs", "/openapi.json"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s: expected status %d, got %d", path, http.StatusOK, response.Code)
		}
		if path == "/openapi.json" {
			document := response.Body.String()
			for _, expected := range []string{
				`"Repository browser"`,
				`"/api/repositories/{repository}/branches"`,
				`"/api/groups/{path}/settings"`,
				`"/api/repositories/{repository}/branches/{branch}"`,
				`"/api/repositories/{repository}/compare"`,
				`"/api/repositories/{repository}/merges"`,
				`"/api/repositories/{repository}/tree/{ref}"`,
				`"/api/repositories/{repository}/tree/{ref}/{path}"`,
				`"/api/repositories/{repository}/blob/{ref}/{path}"`,
				`"/api/repositories/{repository}/files/{ref}/{path}"`,
				`"/api/repositories/{repository}/commits/{ref}"`,
				`"/api/repositories/{repository}/commits/{commit}/diff"`,
			} {
				if !strings.Contains(document, expected) {
					t.Fatalf("OpenAPI document does not contain %s", expected)
				}
			}
		}
	}
}
