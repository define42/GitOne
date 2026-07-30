package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
