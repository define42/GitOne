package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/define42/GitOne/internal/control"
	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/storage"
)

func TestCreateGroupUsesAuthenticatedUserAsOwner(t *testing.T) {
	root := t.TempDir()
	handler := New(Config{
		Root:           root,
		BootstrapUser:  "alice",
		BootstrapToken: "secret",
	})
	request := httptest.NewRequest(http.MethodPost, "/api/groups/engineering", nil)
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

func TestLegacyCreateGroupEndpointIsRemoved(t *testing.T) {
	root := t.TempDir()
	handler := New(Config{
		Root:           root,
		BootstrapUser:  "alice",
		BootstrapToken: "secret",
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
		Root:           root,
		BootstrapUser:  "alice",
		BootstrapToken: "secret",
	})

	createGroup := httptest.NewRequest(http.MethodPost, "/api/groups/engineering", nil)
	createGroup.SetBasicAuth("alice", "secret")
	groupResponse := httptest.NewRecorder()
	handler.ServeHTTP(groupResponse, createGroup)
	if groupResponse.Code != http.StatusCreated {
		t.Fatalf("create group: status %d: %s", groupResponse.Code, groupResponse.Body.String())
	}

	createRepository := httptest.NewRequest(http.MethodPost, "/api/repositories/engineering%2Fapi", nil)
	createRepository.SetBasicAuth("alice", "secret")
	repositoryResponse := httptest.NewRecorder()
	handler.ServeHTTP(repositoryResponse, createRepository)
	if repositoryResponse.Code != http.StatusCreated {
		t.Fatalf("create repository: status %d: %s", repositoryResponse.Code, repositoryResponse.Body.String())
	}

	if _, err := os.Stat(filepath.Join(root, "engineering", "api.git")); err != nil {
		t.Fatalf("repository was not created: %v", err)
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

func TestHumaGroupNavigationAPI(t *testing.T) {
	root := t.TempDir()
	store := storage.Store{Root: root}
	if err := store.CreateGroup("engineering", "alice"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateGroup("engineering/backend", "alice"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRepository(repopath.Repository{Groups: []string{"engineering"}, Name: "web"}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRepository(repopath.Repository{Groups: []string{"engineering", "backend"}, Name: "api"}); err != nil {
		t.Fatal(err)
	}
	handler := New(Config{
		Root:           root,
		BootstrapUser:  "alice",
		BootstrapToken: "secret",
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
			Name string `json:"name"`
			Path string `json:"path"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(listResponse.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Groups) != 1 || list.Groups[0].Path != "engineering" {
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
		Subgroups []struct {
			Path string `json:"path"`
		} `json:"subgroups"`
		Repositories []string `json:"repositories"`
	}
	if err := json.Unmarshal(parentResponse.Body.Bytes(), &parent); err != nil {
		t.Fatal(err)
	}
	if len(parent.Subgroups) != 1 ||
		parent.Subgroups[0].Path != "engineering/backend" ||
		len(parent.Repositories) != 1 ||
		parent.Repositories[0] != "web" {
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
		Path         string   `json:"path"`
		Repositories []string `json:"repositories"`
	}
	if err := json.Unmarshal(detailResponse.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Path != "engineering/backend" ||
		len(detail.Repositories) != 1 ||
		detail.Repositories[0] != "api" {
		t.Fatalf("unexpected group detail: %#v", detail)
	}
}

func TestTypeScriptUIAndHumaDocs(t *testing.T) {
	handler := New(Config{
		Root:           t.TempDir(),
		BootstrapUser:  "alice",
		BootstrapToken: "secret",
	})

	unauthenticated := httptest.NewRequest(http.MethodGet, "/", nil)
	unauthenticatedResponse := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticatedResponse, unauthenticated)
	if unauthenticatedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, unauthenticatedResponse.Code)
	}
	if unauthenticatedResponse.Header().Get("WWW-Authenticate") == "" {
		t.Fatal("missing Basic Auth challenge")
	}

	for _, path := range []string{"/", "/groups/engineering/backend"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.SetBasicAuth("alice", "secret")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s: expected status %d, got %d", path, http.StatusOK, response.Code)
		}
		body := response.Body.String()
		if !strings.Contains(body, `<main id="app"`) ||
			!strings.Contains(body, `<script type="module" src="/assets/app.js?v=3">`) {
			t.Fatalf("%s did not serve the TypeScript UI shell", path)
		}
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
	if !strings.Contains(assetResponse.Body.String(), "request(apiGroupURL(name), { method: \"POST\" })") {
		t.Fatal("served UI does not use the path-based group creation endpoint")
	}
	if !strings.Contains(assetResponse.Body.String(), "navigator.clipboard") ||
		!strings.Contains(assetResponse.Body.String(), "repositoryURL(data.path, repository)") {
		t.Fatal("served UI does not provide copyable full repository URLs")
	}

	for _, path := range []string{"/docs", "/openapi.json"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s: expected status %d, got %d", path, http.StatusOK, response.Code)
		}
	}
}
