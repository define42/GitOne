package lfs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/define42/GitOne/internal/control"
	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/storage"
)

func TestUploadAndDownload(t *testing.T) {
	root := t.TempDir()
	st := storage.Store{Root: root}
	if e := os.MkdirAll(root+"/g/r.lfs/objects", 0o750); e != nil {
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
	_ = os.MkdirAll(root+"/g/r.lfs/objects", 0o750)
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

func TestProtocolRoutesAndFailures(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(root+"/g/r.lfs/objects", 0o750); err != nil {
		t.Fatal(err)
	}
	allowed := Handler{
		Storage: storage.Store{Root: root},
		Authorize: func(*http.Request, repopath.Repository, bool) (bool, bool) {
			return true, true
		},
	}
	request := func(handler Handler, method, path string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(method, path, nil))
		return response
	}
	missingOID := string(bytes.Repeat([]byte{'0'}, 64))

	for _, test := range []struct {
		name, method, path string
		status             int
	}{
		{
			name:   "invalid repository path",
			method: http.MethodGet,
			path:   "/r.git/info/lfs/objects/" + missingOID,
			status: http.StatusBadRequest,
		},
		{
			name:   "unknown LFS route",
			method: http.MethodGet,
			path:   "/g/r.git",
			status: http.StatusNotFound,
		},
		{
			name:   "verify",
			method: http.MethodPost,
			path:   "/g/r.git/info/lfs/objects/verify",
			status: http.StatusOK,
		},
		{
			name:   "invalid object ID",
			method: http.MethodGet,
			path:   "/g/r.git/info/lfs/objects/invalid",
			status: http.StatusBadRequest,
		},
		{
			name:   "missing object",
			method: http.MethodGet,
			path:   "/g/r.git/info/lfs/objects/" + missingOID,
			status: http.StatusNotFound,
		},
		{
			name:   "unsupported object method",
			method: http.MethodPost,
			path:   "/g/r.git/info/lfs/objects/" + missingOID,
			status: http.StatusMethodNotAllowed,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := request(allowed, test.method, test.path)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.status, response.Body.String())
			}
		})
	}

	denied := allowed
	denied.Authorize = func(*http.Request, repopath.Repository, bool) (bool, bool) {
		return false, false
	}
	response := request(denied, http.MethodPost, "/g/r.git/info/lfs/objects/verify")
	if response.Code != http.StatusUnauthorized ||
		response.Header().Get("WWW-Authenticate") != `Basic realm="GitOne"` {
		t.Fatalf("denied verify returned %d with challenge %q", response.Code, response.Header().Get("WWW-Authenticate"))
	}

	failedPolicy := allowed
	failedPolicy.Policy = func(*http.Request, repopath.Repository) (control.RepositoryPolicy, error) {
		return control.RepositoryPolicy{}, errors.New("control repository unavailable")
	}
	response = request(failedPolicy, http.MethodPost, "/g/r.git/info/lfs/objects/verify")
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("failed policy returned %d: %s", response.Code, response.Body.String())
	}
}

func TestOpenObjectValidatesAndReadsStoredObject(t *testing.T) {
	store := storage.Store{Root: t.TempDir()}
	repository := repopath.Repository{Groups: []string{"engineering"}, Name: "docs"}
	content := []byte("stored LFS object")
	sum := sha256.Sum256(content)
	oid := hex.EncodeToString(sum[:])

	if _, err := OpenObject(store, repository, "invalid"); err == nil {
		t.Fatal("invalid object ID was accepted")
	}
	if _, err := OpenObject(store, repository, oid); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing object returned %v", err)
	}
	path, err := objectPath(store, repository, oid)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, content, 0o640); err != nil {
		t.Fatal(err)
	}
	file, err := OpenObject(store, repository, oid)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = file.Close()
	}()
	got, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("OpenObject() read %q, want %q", got, content)
	}
}
