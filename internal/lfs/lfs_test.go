package lfs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"github.com/define42/GitOne/internal/control"
	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/storage"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestUploadAndDownload(t *testing.T) {
	root := t.TempDir()
	st := storage.Store{Root: root}
	if e := os.MkdirAll(root+"/g/r.lfs/objects", 0750); e != nil {
		t.Fatal(e)
	}
	data := []byte("large object")
	sum := sha256.Sum256(data)
	oid := hex.EncodeToString(sum[:])
	h := Handler{Storage: st, PublicURL: "http://example", Authorize: func(*http.Request, repopath.Repository, bool) (bool, bool) { return true, true }}
	put := httptest.NewRequest(http.MethodPut, "/g/r.git/info/lfs/objects/"+oid, bytes.NewReader(data))
	pw := httptest.NewRecorder()
	h.ServeHTTP(pw, put)
	if pw.Code != 200 {
		t.Fatalf("put=%d %s", pw.Code, pw.Body.String())
	}
	get := httptest.NewRequest(http.MethodGet, "/g/r.git/info/lfs/objects/"+oid, nil)
	gw := httptest.NewRecorder()
	h.ServeHTTP(gw, get)
	if gw.Code != 200 || !bytes.Equal(gw.Body.Bytes(), data) {
		t.Fatalf("get=%d %q", gw.Code, gw.Body.Bytes())
	}
}
func TestRejectWrongHash(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(root+"/g/r.lfs/objects", 0750)
	h := Handler{Storage: storage.Store{Root: root}, Authorize: func(*http.Request, repopath.Repository, bool) (bool, bool) { return true, true }}
	oid := string(bytes.Repeat([]byte{'0'}, 64))
	r := httptest.NewRequest(http.MethodPut, "/g/r.git/info/lfs/objects/"+oid, bytes.NewBufferString("wrong"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 422 {
		t.Fatalf("expected 422, got %d", w.Code)
	}
}

func TestPolicyDisablesLFS(t *testing.T) {
	root := t.TempDir()
	h := Handler{
		Storage: storage.Store{Root: root},
		Authorize: func(*http.Request, repopath.Repository, bool) (bool, bool) {
			return true, true
		},
		Policy: func(*http.Request, repopath.Repository) (control.RepositoryPolicy, error) {
			return control.RepositoryPolicy{}, nil
		},
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/g/r.git/info/lfs/objects/batch",
		bytes.NewBufferString(`{"operation":"download","objects":[]}`),
	)
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected disabled LFS to return 403, got %d", response.Code)
	}
}

func TestUploadEnforcesObjectAndStorageLimits(t *testing.T) {
	root := t.TempDir()
	h := Handler{
		Storage: storage.Store{Root: root},
		Authorize: func(*http.Request, repopath.Repository, bool) (bool, bool) {
			return true, true
		},
		Policy: func(*http.Request, repopath.Repository) (control.RepositoryPolicy, error) {
			return control.RepositoryPolicy{LFS: control.LFSPolicy{
				Enabled:             true,
				MaximumObjectBytes:  4,
				MaximumStorageBytes: 6,
			}}, nil
		},
	}
	upload := func(data string) *httptest.ResponseRecorder {
		sum := sha256.Sum256([]byte(data))
		oid := hex.EncodeToString(sum[:])
		request := httptest.NewRequest(
			http.MethodPut,
			"/g/r.git/info/lfs/objects/"+oid,
			bytes.NewBufferString(data),
		)
		response := httptest.NewRecorder()
		h.ServeHTTP(response, request)
		return response
	}

	if response := upload("12345"); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("oversized object returned %d: %s", response.Code, response.Body.String())
	}
	if response := upload("1234"); response.Code != http.StatusOK {
		t.Fatalf("first object returned %d: %s", response.Code, response.Body.String())
	}
	if response := upload("56"); response.Code != http.StatusOK {
		t.Fatalf("object at storage limit returned %d: %s", response.Code, response.Body.String())
	}
	if response := upload("7"); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("storage overflow returned %d: %s", response.Code, response.Body.String())
	}
}
