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
