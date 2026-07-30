package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/define42/GitOne/internal/repopath"
	git "github.com/go-git/go-git/v5"
)

func TestImportRemoteLFSWithoutPointersDoesNotContactEndpoint(t *testing.T) {
	var requests atomic.Int64
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	defer remote.Close()

	sourceRoot := t.TempDir()
	sourceStore := Store{Root: sourceRoot}
	if err := sourceStore.CreateGroup("source", "alice", ""); err != nil {
		t.Fatal(err)
	}
	source := repopath.Repository{Groups: []string{"source"}, Name: "api"}
	if err := sourceStore.CreateRepository(source, CreateRepositoryOptions{
		InitializeReadme: true,
		Author:           "alice",
	}); err != nil {
		t.Fatal(err)
	}
	gitPath, err := sourceStore.GitPath(source)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := git.PlainOpen(gitPath)
	if err != nil {
		t.Fatal(err)
	}
	lfsPath := filepath.Join(t.TempDir(), "repository.lfs")
	if err = importRemoteLFS(
		context.Background(),
		repository,
		ImportRepositoryOptions{URL: remote.URL + "/source/api.git"},
		lfsPath,
	); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 0 {
		t.Fatalf("LFS endpoint received %d requests", requests.Load())
	}
	if _, err = os.Stat(filepath.Join(lfsPath, "objects")); err != nil {
		t.Fatalf("LFS objects directory: %v", err)
	}
}

func TestDownloadLFSObjectRejectsCorruptContent(t *testing.T) {
	const (
		expectedContent = "expected LFS bytes"
		corruptContent  = "corrupt! LFS bytes"
	)
	digest := sha256.Sum256([]byte(expectedContent))
	oid := hex.EncodeToString(digest[:])

	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok || username != "alice" || password != "secret" {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("X-LFS-Token") != "download-token" {
			http.Error(w, "missing action header", http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(corruptContent))
	}))
	defer remote.Close()

	policy, err := NewImportNetworkPolicy([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	batchURL, err := url.Parse(remote.URL + "/source/api.git/info/lfs/objects/batch")
	if err != nil {
		t.Fatal(err)
	}
	lfsPath := filepath.Join(t.TempDir(), "repository.lfs")
	err = downloadLFSObject(
		WithImportNetworkPolicy(context.Background(), policy),
		newImportHTTPClient(),
		batchURL,
		ImportRepositoryOptions{
			Username: "alice",
			Password: "secret",
		},
		lfsPath,
		importLFSObject{OID: oid, Size: int64(len(expectedContent))},
		importLFSAction{
			Href: remote.URL + "/objects/" + oid,
			Header: map[string]string{
				"X-LFS-Token": "download-token",
			},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "SHA-256 verification") {
		t.Fatalf("downloadLFSObject() error = %v", err)
	}
	objectPath := filepath.Join(lfsPath, "objects", oid[:2], oid[2:4], oid)
	if _, statErr := os.Stat(objectPath); !os.IsNotExist(statErr) {
		t.Fatalf("corrupt LFS object was published: %v", statErr)
	}
}

func TestDownloadLFSBatchDoesNotForwardCredentialsAcrossOrigins(t *testing.T) {
	content := []byte("remote LFS object")
	digest := sha256.Sum256(content)
	oid := hex.EncodeToString(digest[:])

	var downloadAuthorization atomic.Value
	download := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		downloadAuthorization.Store(request.Header.Get("Authorization"))
		if request.Header.Get("X-LFS-Token") != "download-token" {
			http.Error(response, "missing action token", http.StatusForbidden)
			return
		}
		_, _ = response.Write(content)
	}))
	defer download.Close()

	batch := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		username, password, ok := request.BasicAuth()
		if !ok || username != "alice" || password != "secret" {
			http.Error(response, "missing batch credentials", http.StatusUnauthorized)
			return
		}
		if request.Header.Get("Accept") != "application/vnd.git-lfs+json" ||
			request.Header.Get("Content-Type") != "application/vnd.git-lfs+json" {
			http.Error(response, "invalid LFS headers", http.StatusBadRequest)
			return
		}
		var payload importLFSBatchRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			http.Error(response, err.Error(), http.StatusBadRequest)
			return
		}
		if payload.Operation != "download" ||
			len(payload.Objects) != 1 ||
			payload.Objects[0].OID != oid {
			http.Error(response, "invalid batch payload", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(response).Encode(importLFSBatchResponse{
			Transfer: "basic",
			Objects: []importLFSBatchObject{{
				OID:  oid,
				Size: int64(len(content)),
				Actions: map[string]importLFSAction{
					"download": {
						Href: download.URL + "/objects/" + oid,
						Header: map[string]string{
							"X-LFS-Token": "download-token",
						},
					},
				},
			}},
		})
	}))
	defer batch.Close()

	batchURL, err := url.Parse(batch.URL + "/repository.git/info/lfs/objects/batch")
	if err != nil {
		t.Fatal(err)
	}
	lfsPath := filepath.Join(t.TempDir(), "repository.lfs")
	if err = downloadLFSBatch(
		context.Background(),
		http.DefaultClient,
		batchURL,
		ImportRepositoryOptions{Username: "alice", Password: "secret"},
		lfsPath,
		[]importLFSObject{{OID: oid, Size: int64(len(content))}},
	); err != nil {
		t.Fatal(err)
	}
	authorization, _ := downloadAuthorization.Load().(string)
	if authorization != "" {
		t.Fatalf("cross-origin download received credentials %q", authorization)
	}
	objectPath := filepath.Join(lfsPath, "objects", oid[:2], oid[2:4], oid)
	stored, err := os.ReadFile(objectPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, content) {
		t.Fatalf("stored LFS object = %q, want %q", stored, content)
	}
	info, err := os.Stat(objectPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("stored LFS object mode = %o, want 640", info.Mode().Perm())
	}
}

func TestDownloadLFSBatchRejectsInvalidResponses(t *testing.T) {
	content := []byte("batch object")
	digest := sha256.Sum256(content)
	oid := hex.EncodeToString(digest[:])
	expected := []importLFSObject{{OID: oid, Size: int64(len(content))}}

	encode := func(value importLFSBatchResponse) []byte {
		t.Helper()
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	validObject := func(serverURL string) importLFSBatchObject {
		return importLFSBatchObject{
			OID:  oid,
			Size: int64(len(content)),
			Actions: map[string]importLFSAction{
				"download": {Href: serverURL + "/object"},
			},
		}
	}
	tests := []struct {
		name   string
		status int
		body   func(string) []byte
		want   string
	}{
		{
			name: "HTTP failure", status: http.StatusBadGateway,
			body: func(string) []byte { return []byte(`{}`) },
			want: "HTTP 502",
		},
		{
			name: "invalid JSON", status: http.StatusOK,
			body: func(string) []byte { return []byte(`{`) },
			want: "response is invalid",
		},
		{
			name: "oversized response", status: http.StatusOK,
			body: func(string) []byte {
				return bytes.Repeat([]byte{' '}, importLFSMaximumResponse+1)
			},
			want: "response is too large",
		},
		{
			name: "unsupported transfer", status: http.StatusOK,
			body: func(string) []byte {
				return encode(importLFSBatchResponse{Transfer: "ssh"})
			},
			want: "not supported",
		},
		{
			name: "unexpected object", status: http.StatusOK,
			body: func(string) []byte {
				return encode(importLFSBatchResponse{
					Objects: []importLFSBatchObject{{OID: strings.Repeat("0", 64)}},
				})
			},
			want: "unexpected object",
		},
		{
			name: "duplicate object", status: http.StatusOK,
			body: func(serverURL string) []byte {
				object := validObject(serverURL)
				return encode(importLFSBatchResponse{
					Objects: []importLFSBatchObject{object, object},
				})
			},
			want: "duplicate object",
		},
		{
			name: "size mismatch", status: http.StatusOK,
			body: func(serverURL string) []byte {
				object := validObject(serverURL)
				object.Size++
				return encode(importLFSBatchResponse{
					Objects: []importLFSBatchObject{object},
				})
			},
			want: "unexpected size",
		},
		{
			name: "object error", status: http.StatusOK,
			body: func(serverURL string) []byte {
				object := validObject(serverURL)
				object.Error = &importLFSObjectError{
					Code: http.StatusNotFound, Message: "missing",
				}
				return encode(importLFSBatchResponse{
					Objects: []importLFSBatchObject{object},
				})
			},
			want: "is unavailable",
		},
		{
			name: "missing action", status: http.StatusOK,
			body: func(string) []byte {
				return encode(importLFSBatchResponse{
					Objects: []importLFSBatchObject{{
						OID: oid, Size: int64(len(content)),
					}},
				})
			},
			want: "no download action",
		},
		{
			name: "omitted object", status: http.StatusOK,
			body: func(string) []byte {
				return encode(importLFSBatchResponse{})
			},
			want: "omitted object",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(
				response http.ResponseWriter,
				request *http.Request,
			) {
				if request.Method == http.MethodGet {
					_, _ = response.Write(content)
					return
				}
				response.WriteHeader(test.status)
				_, _ = response.Write(test.body(server.URL))
			}))
			defer server.Close()

			batchURL, err := url.Parse(server.URL + "/objects/batch")
			if err != nil {
				t.Fatal(err)
			}
			err = downloadLFSBatch(
				context.Background(),
				http.DefaultClient,
				batchURL,
				ImportRepositoryOptions{},
				filepath.Join(t.TempDir(), "repository.lfs"),
				expected,
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("downloadLFSBatch() error = %v, want %q", err, test.want)
			}
		})
	}
}
