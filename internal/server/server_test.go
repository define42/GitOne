package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
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
	request := httptest.NewRequest(http.MethodPost, "/api/groups", bytes.NewBufferString(`{"path":"engineering"}`))
	request.Header.Set("Content-Type", "application/json")
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

func TestCreateGroupRejectsOwnerFields(t *testing.T) {
	handler := New(Config{
		Root:           t.TempDir(),
		BootstrapUser:  "alice",
		BootstrapToken: "secret",
	})
	request := httptest.NewRequest(http.MethodPost, "/api/groups", bytes.NewBufferString(`{"path":"engineering","owner":"mallory"}`))
	request.Header.Set("Content-Type", "application/json")
	request.SetBasicAuth("alice", "secret")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d: %s", http.StatusBadRequest, response.Code, response.Body.String())
	}
}
