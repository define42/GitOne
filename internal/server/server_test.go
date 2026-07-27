package server

import (
	"context"
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
	request := httptest.NewRequest(http.MethodPost, "/api/groups", strings.NewReader("path=engineering"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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
}

func TestCreateGroupRejectsUnknownFormFields(t *testing.T) {
	handler := New(Config{
		Root:           t.TempDir(),
		BootstrapUser:  "alice",
		BootstrapToken: "secret",
	})
	request := httptest.NewRequest(http.MethodPost, "/api/groups", strings.NewReader("path=engineering&owner=mallory"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.SetBasicAuth("alice", "secret")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d: %s", http.StatusBadRequest, response.Code, response.Body.String())
	}
}

func TestCreateRepositoryFromURLEncodedForm(t *testing.T) {
	root := t.TempDir()
	handler := New(Config{
		Root:           root,
		BootstrapUser:  "alice",
		BootstrapToken: "secret",
	})

	createGroup := httptest.NewRequest(http.MethodPost, "/api/groups", strings.NewReader("path=engineering"))
	createGroup.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	createGroup.SetBasicAuth("alice", "secret")
	groupResponse := httptest.NewRecorder()
	handler.ServeHTTP(groupResponse, createGroup)
	if groupResponse.Code != http.StatusCreated {
		t.Fatalf("create group: status %d: %s", groupResponse.Code, groupResponse.Body.String())
	}

	createRepository := httptest.NewRequest(http.MethodPost, "/api/repositories", strings.NewReader("group=engineering&name=api"))
	createRepository.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	createRepository.SetBasicAuth("alice", "secret")
	repositoryResponse := httptest.NewRecorder()
	handler.ServeHTTP(repositoryResponse, createRepository)
	if repositoryResponse.Code != http.StatusCreated {
		t.Fatalf("create repository: status %d: %s", repositoryResponse.Code, repositoryResponse.Body.String())
	}

	if _, err := os.Stat(filepath.Join(root, "engineering", "api.git")); err != nil {
		t.Fatalf("repository was not created: %v", err)
	}
}

func TestWebUIUsesBasicAuthAndHTMLForms(t *testing.T) {
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

	unauthenticated := httptest.NewRequest(http.MethodGet, "/", nil)
	unauthenticatedResponse := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticatedResponse, unauthenticated)
	if unauthenticatedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, unauthenticatedResponse.Code)
	}
	if unauthenticatedResponse.Header().Get("WWW-Authenticate") == "" {
		t.Fatal("missing Basic Auth challenge")
	}

	authenticated := httptest.NewRequest(http.MethodGet, "/", nil)
	authenticated.SetBasicAuth("alice", "secret")
	authenticatedResponse := httptest.NewRecorder()
	handler.ServeHTTP(authenticatedResponse, authenticated)
	if authenticatedResponse.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, authenticatedResponse.Code)
	}
	rootBody := authenticatedResponse.Body.String()
	for _, expected := range []string{
		`<a href="/groups/engineering">engineering</a>`,
		`<form method="post" action="/ui/groups">`,
	} {
		if !strings.Contains(rootBody, expected) {
			t.Fatalf("root page does not contain %q", expected)
		}
	}
	for _, unexpected := range []string{
		`/groups/engineering/backend`,
		`<code>web.git</code>`,
		`action="/ui/subgroups"`,
		`action="/ui/repositories"`,
	} {
		if strings.Contains(rootBody, unexpected) {
			t.Fatalf("root page unexpectedly contains %q", unexpected)
		}
	}

	groupRequest := httptest.NewRequest(http.MethodGet, "/groups/engineering", nil)
	groupRequest.SetBasicAuth("alice", "secret")
	groupResponse := httptest.NewRecorder()
	handler.ServeHTTP(groupResponse, groupRequest)
	if groupResponse.Code != http.StatusOK {
		t.Fatalf("group page: expected status %d, got %d", http.StatusOK, groupResponse.Code)
	}
	groupBody := groupResponse.Body.String()
	for _, expected := range []string{
		`<a href="/groups/engineering/backend">backend</a>`,
		`<code>web.git</code>`,
		`<form method="post" action="/ui/subgroups">`,
		`<form method="post" action="/ui/repositories">`,
	} {
		if !strings.Contains(groupBody, expected) {
			t.Fatalf("group page does not contain %q", expected)
		}
	}
	if strings.Contains(groupBody, `<code>api.git</code>`) {
		t.Fatal("parent group page must not list a subgroup repository")
	}

	subgroupRequest := httptest.NewRequest(http.MethodGet, "/groups/engineering/backend", nil)
	subgroupRequest.SetBasicAuth("alice", "secret")
	subgroupResponse := httptest.NewRecorder()
	handler.ServeHTTP(subgroupResponse, subgroupRequest)
	if subgroupResponse.Code != http.StatusOK {
		t.Fatalf("subgroup page: expected status %d, got %d", http.StatusOK, subgroupResponse.Code)
	}
	if !strings.Contains(subgroupResponse.Body.String(), `<code>api.git</code>`) {
		t.Fatal("subgroup page does not contain its repository")
	}

	if strings.Contains(strings.ToLower(rootBody+groupBody+subgroupResponse.Body.String()), "<script") {
		t.Fatal("page must not contain JavaScript")
	}
}

func TestWebUIFormRedirectsAfterCreation(t *testing.T) {
	handler := New(Config{
		Root:           t.TempDir(),
		BootstrapUser:  "alice",
		BootstrapToken: "secret",
	})
	tests := []struct {
		name     string
		path     string
		form     string
		location string
	}{
		{
			name:     "top-level group",
			path:     "/ui/groups",
			form:     "name=engineering",
			location: "/?created=group",
		},
		{
			name:     "subgroup",
			path:     "/ui/subgroups",
			form:     "parent=engineering&name=backend",
			location: "/groups/engineering?created=subgroup",
		},
		{
			name:     "repository",
			path:     "/ui/repositories",
			form:     "group=engineering%2Fbackend&name=api",
			location: "/groups/engineering/backend?created=repository",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.form))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.SetBasicAuth("alice", "secret")
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusSeeOther {
				t.Fatalf("expected status %d, got %d: %s", http.StatusSeeOther, response.Code, response.Body.String())
			}
			if location := response.Header().Get("Location"); location != test.location {
				t.Fatalf("unexpected redirect location %q", location)
			}
		})
	}
}
