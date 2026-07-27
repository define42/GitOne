package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/define42/GitOne/internal/control"
	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/storage"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	gittransport "github.com/go-git/go-git/v5/plumbing/transport/http"
)

func TestCreateGroupUsesAuthenticatedUserAsOwner(t *testing.T) {
	root := t.TempDir()
	handler := New(Config{
		Root:           root,
		BootstrapUser:  "alice",
		BootstrapToken: "secret",
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
		Root:           t.TempDir(),
		BootstrapUser:  "alice",
		BootstrapToken: "secret",
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
			!strings.Contains(body, `<script type="module" src="/assets/app.js?v=10">`) {
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
	if !strings.Contains(assetResponse.Body.String(), "`${apiGroupURL(name)}?description=${encodeURIComponent(description.input.value)}`") {
		t.Fatal("served UI does not use the path-based group creation endpoint")
	}
	if !strings.Contains(assetResponse.Body.String(), "navigator.clipboard") ||
		!strings.Contains(assetResponse.Body.String(), "repositoryURL(data.path, repository.name, data.username)") {
		t.Fatal("served UI does not provide copyable full repository URLs")
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
	if !strings.Contains(assetResponse.Body.String(), "description.input.value") ||
		!strings.Contains(assetResponse.Body.String(), "subgroupDescription.input.value") {
		t.Fatal("served UI does not provide group and subgroup descriptions")
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
	}
}
