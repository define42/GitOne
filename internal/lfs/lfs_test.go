package lfs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/define42/GitOne/internal/control"
	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/review"
	"github.com/define42/GitOne/internal/storage"
	git "github.com/go-git/go-git/v5"
)

type stagedUploadReader struct {
	data    []byte
	offset  int
	staged  chan<- struct{}
	release <-chan struct{}
}

func (r *stagedUploadReader) Read(buffer []byte) (int, error) {
	if r.offset < len(r.data) {
		n := copy(buffer, r.data[r.offset:])
		r.offset += n
		return n, nil
	}
	select {
	case r.staged <- struct{}{}:
	default:
	}
	<-r.release
	return 0, io.EOF
}

func TestUploadAndDownload(t *testing.T) {
	root := t.TempDir()
	initializeLFSRepository(t, root)
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
	initializeLFSRepository(t, root)
	_ = os.MkdirAll(root+"/g/r.lfs/objects", 0o750)
	h := Handler{Storage: storage.Store{Root: root}, Authorize: func(*http.Request, repopath.Repository, bool) (bool, bool) { return true, true }}
	oid := string(bytes.Repeat([]byte{'0'}, 64))
	r := httptest.NewRequest(http.MethodPut, "/g/r.git/info/lfs/objects/"+oid, bytes.NewBufferString("wrong"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 422 {
		t.Fatalf("expected 422, got %d", w.Code)
	}
	entries, err := os.ReadDir(filepath.Join(root, ".gitone", "uploads"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed upload left %d staged object(s)", len(entries))
	}
}

func TestPolicyDisablesLFS(t *testing.T) {
	root := t.TempDir()
	h := Handler{
		Storage: storage.Store{Root: root},
		Authorize: func(*http.Request, repopath.Repository, bool) (bool, bool) {
			return true, true
		},
		Policy: func(*http.Request, repopath.Repository) (control.LFSPolicy, error) {
			return control.LFSPolicy{}, nil
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
	initializeLFSRepository(t, root)
	h := Handler{
		Storage: storage.Store{Root: root},
		Authorize: func(*http.Request, repopath.Repository, bool) (bool, bool) {
			return true, true
		},
		Policy: func(*http.Request, repopath.Repository) (control.LFSPolicy, error) {
			return control.LFSPolicy{
				Enabled:             true,
				MaximumObjectBytes:  4,
				MaximumStorageBytes: 6,
			}, nil
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
		request.ContentLength = -1
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

func TestUploadStagingBoundedByStorageQuotaWithoutObjectLimit(t *testing.T) {
	root := t.TempDir()
	initializeLFSRepository(t, root)
	h := Handler{
		Storage: storage.Store{Root: root},
		Authorize: func(*http.Request, repopath.Repository, bool) (bool, bool) {
			return true, true
		},
		Policy: func(*http.Request, repopath.Repository) (control.LFSPolicy, error) {
			// MaximumObjectBytes unset (0) is a common configuration; the
			// group storage quota must still bound how much a single upload
			// streams to the staging directory.
			return control.LFSPolicy{Enabled: true, MaximumStorageBytes: 16}, nil
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
		request.ContentLength = -1
		response := httptest.NewRecorder()
		h.ServeHTTP(response, request)
		return response
	}
	if response := upload("far larger than the storage quota"); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("upload exceeding storage quota returned %d: %s", response.Code, response.Body.String())
	}
	// A small object within the quota still streams through the
	// storage-quota-bounded staging path and is stored successfully.
	if response := upload("small"); response.Code != http.StatusOK {
		t.Fatalf("upload within storage quota returned %d: %s", response.Code, response.Body.String())
	}
}

func TestConcurrentUploadsReserveQuotaBeforeStaging(t *testing.T) {
	root := t.TempDir()
	initializeLFSRepository(t, root)
	const quota = int64(8)
	handler := Handler{
		Storage: storage.Store{Root: root},
		Authorize: func(*http.Request, repopath.Repository, bool) (bool, bool) {
			return true, true
		},
		Policy: func(*http.Request, repopath.Repository) (control.LFSPolicy, error) {
			return control.LFSPolicy{Enabled: true, MaximumStorageBytes: quota}, nil
		},
	}

	startUpload := func(
		data []byte,
		staged chan<- struct{},
		release <-chan struct{},
	) <-chan *httptest.ResponseRecorder {
		t.Helper()
		sum := sha256.Sum256(data)
		oid := hex.EncodeToString(sum[:])
		request := httptest.NewRequest(
			http.MethodPut,
			"/g/r.git/info/lfs/objects/"+oid,
			&stagedUploadReader{
				data:    data,
				staged:  staged,
				release: release,
			},
		)
		request.ContentLength = int64(len(data))
		response := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			response <- recorder
		}()
		return response
	}

	firstStaged := make(chan struct{}, 1)
	firstRelease := make(chan struct{})
	var releaseFirst sync.Once
	unblockFirst := func() {
		releaseFirst.Do(func() {
			close(firstRelease)
		})
	}
	defer unblockFirst()
	firstResponse := startUpload(
		[]byte("12345678"),
		firstStaged,
		firstRelease,
	)
	select {
	case <-firstStaged:
	case <-time.After(2 * time.Second):
		t.Fatal("first upload did not fill its reserved staging space")
	}

	stagingDirectory := filepath.Join(root, ".gitone", "uploads")
	entries, err := os.ReadDir(stagingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("active upload created %d staging files, want 1", len(entries))
	}
	info, err := entries[0].Info()
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != quota {
		t.Fatalf("staged bytes = %d, want %d", info.Size(), quota)
	}

	secondStaged := make(chan struct{}, 1)
	secondRelease := make(chan struct{})
	var releaseSecond sync.Once
	unblockSecond := func() {
		releaseSecond.Do(func() {
			close(secondRelease)
		})
	}
	defer unblockSecond()
	secondResponse := startUpload(
		[]byte("abcdefgh"),
		secondStaged,
		secondRelease,
	)
	select {
	case <-secondStaged:
		t.Fatal("second upload bypassed the first upload's quota reservation")
	case response := <-secondResponse:
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("concurrent quota overflow returned %d: %s", response.Code, response.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second upload was not rejected during quota admission")
	}

	entries, err = os.ReadDir(stagingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("quota-rejected upload left %d active staging files, want 1", len(entries))
	}

	unblockFirst()
	select {
	case response := <-firstResponse:
		if response.Code != http.StatusOK {
			t.Fatalf("reserved upload returned %d: %s", response.Code, response.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reserved upload did not finish")
	}
	entries, err = os.ReadDir(stagingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("completed concurrent uploads left %d staging files", len(entries))
	}
}

func TestBatchRejectsSizeExceedingStorageQuota(t *testing.T) {
	root := t.TempDir()
	initializeLFSRepository(t, root)
	h := Handler{
		Storage: storage.Store{Root: root},
		Authorize: func(*http.Request, repopath.Repository, bool) (bool, bool) {
			return true, true
		},
		Policy: func(*http.Request, repopath.Repository) (control.LFSPolicy, error) {
			return control.LFSPolicy{Enabled: true, MaximumStorageBytes: 8}, nil
		},
	}
	oid := hex.EncodeToString(make([]byte, 32))
	// A size that alone exceeds the storage quota must be refused before it is
	// summed into the running total, where MaxInt64 would otherwise overflow.
	body := `{"operation":"upload","objects":[{"oid":"` + oid + `","size":9223372036854775807}]}`
	request := httptest.NewRequest(
		http.MethodPost,
		"/g/r.git/info/lfs/objects/batch",
		bytes.NewBufferString(body),
	)
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("batch returned %d: %s", response.Code, response.Body.String())
	}
	var decoded struct {
		Objects []struct {
			Actions map[string]any `json:"actions"`
			Error   *struct {
				Code int `json:"code"`
			} `json:"error"`
		} `json:"objects"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Objects) != 1 ||
		decoded.Objects[0].Error == nil ||
		decoded.Objects[0].Error.Code != http.StatusUnprocessableEntity ||
		len(decoded.Objects[0].Actions) != 0 {
		t.Fatalf("oversized batch object was not rejected: %s", response.Body.String())
	}
}

func TestUploadEnforcesStorageLimitAcrossGroupRepositories(t *testing.T) {
	root := t.TempDir()
	initializeLFSRepository(t, root)
	existing := []byte("1234")
	existingHash := sha256.Sum256(existing)
	existingOID := hex.EncodeToString(existingHash[:])
	existingPath := filepath.Join(
		root,
		"g",
		"other.lfs",
		"objects",
		existingOID[:2],
		existingOID[2:4],
		existingOID,
	)
	if err := os.MkdirAll(filepath.Dir(existingPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(existingPath, existing, 0o640); err != nil {
		t.Fatal(err)
	}
	handler := Handler{
		Storage: storage.Store{Root: root},
		Authorize: func(*http.Request, repopath.Repository, bool) (bool, bool) {
			return true, true
		},
		Policy: func(*http.Request, repopath.Repository) (control.LFSPolicy, error) {
			return control.LFSPolicy{Enabled: true, MaximumStorageBytes: 6}, nil
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
		handler.ServeHTTP(response, request)
		return response
	}

	if response := upload("567"); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("group storage overflow returned %d: %s", response.Code, response.Body.String())
	}
	if response := upload("56"); response.Code != http.StatusOK {
		t.Fatalf("upload at group storage limit returned %d: %s", response.Code, response.Body.String())
	}
}

func TestUploadWaitsForRepositoryOperationLockBeforeStaging(t *testing.T) {
	root := t.TempDir()
	initializeLFSRepository(t, root)
	data := []byte("operation-locked LFS object")
	sum := sha256.Sum256(data)
	oid := hex.EncodeToString(sum[:])
	var authorizationCalls atomic.Int32
	firstAuthorization := make(chan struct{}, 1)
	handler := Handler{
		Storage: storage.Store{Root: root},
		Authorize: func(*http.Request, repopath.Repository, bool) (bool, bool) {
			authorizationCalls.Add(1)
			select {
			case firstAuthorization <- struct{}{}:
			default:
			}
			return true, true
		},
	}
	release, err := review.NewStore(root).AcquireOperationLock()
	if err != nil {
		t.Fatal(err)
	}
	response := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(
			recorder,
			httptest.NewRequest(
				http.MethodPut,
				"/g/r.git/info/lfs/objects/"+oid,
				bytes.NewReader(data),
			),
		)
		response <- recorder
	}()
	select {
	case <-firstAuthorization:
	case <-time.After(2 * time.Second):
		t.Fatal("upload did not reach initial authorization")
	}
	stagingDirectory := filepath.Join(root, ".gitone", "uploads")
	entries, readErr := os.ReadDir(stagingDirectory)
	if readErr == nil && len(entries) != 0 {
		t.Fatalf("upload staged %d objects before quota admission", len(entries))
	}
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		t.Fatal(readErr)
	}
	select {
	case recorder := <-response:
		t.Fatalf("upload completed while operation lock was held: %d", recorder.Code)
	case <-time.After(100 * time.Millisecond):
	}
	if err = release(); err != nil {
		t.Fatal(err)
	}
	select {
	case recorder := <-response:
		if recorder.Code != http.StatusOK {
			t.Fatalf("upload returned %d: %s", recorder.Code, recorder.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upload did not resume after operation lock release")
	}
	if authorizationCalls.Load() != 2 {
		t.Fatalf("authorization calls = %d, want 2", authorizationCalls.Load())
	}
	entries, err = os.ReadDir(stagingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("successful upload left %d staged object(s)", len(entries))
	}
}

func TestUploadReopensRepositoryAfterOperationLock(t *testing.T) {
	root := t.TempDir()
	initializeLFSRepository(t, root)
	store := storage.Store{Root: root}
	repository := repopath.Repository{Groups: []string{"g"}, Name: "r"}
	data := []byte("stale repository upload")
	sum := sha256.Sum256(data)
	oid := hex.EncodeToString(sum[:])
	firstAuthorization := make(chan struct{}, 1)
	handler := Handler{
		Storage: store,
		Authorize: func(*http.Request, repopath.Repository, bool) (bool, bool) {
			select {
			case firstAuthorization <- struct{}{}:
			default:
			}
			return true, true
		},
	}
	release, err := review.NewStore(root).AcquireOperationLock()
	if err != nil {
		t.Fatal(err)
	}
	response := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(
			recorder,
			httptest.NewRequest(
				http.MethodPut,
				"/g/r.git/info/lfs/objects/"+oid,
				bytes.NewReader(data),
			),
		)
		response <- recorder
	}()
	select {
	case <-firstAuthorization:
	case <-time.After(2 * time.Second):
		t.Fatal("upload did not reach initial authorization")
	}
	gitPath, err := store.GitPath(repository)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(gitPath, gitPath+".moved"); err != nil {
		t.Fatal(err)
	}
	if err = release(); err != nil {
		t.Fatal(err)
	}
	select {
	case recorder := <-response:
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("stale upload returned %d: %s", recorder.Code, recorder.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stale upload did not finish")
	}
	objectPath, err := handler.objectPath(repository, oid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(objectPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale LFS object exists: %v", err)
	}
}

func initializeLFSRepository(t *testing.T, root string) {
	t.Helper()
	path, err := (storage.Store{Root: root}).GitPath(repopath.Repository{
		Groups: []string{"g"},
		Name:   "r",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err = git.PlainInit(path, true); err != nil {
		t.Fatal(err)
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
			name:   "malformed verify",
			method: http.MethodPost,
			path:   "/g/r.git/info/lfs/objects/verify",
			status: http.StatusBadRequest,
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
	failedPolicy.Policy = func(*http.Request, repopath.Repository) (control.LFSPolicy, error) {
		return control.LFSPolicy{}, errors.New("control repository unavailable")
	}
	response = request(failedPolicy, http.MethodPost, "/g/r.git/info/lfs/objects/verify")
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("failed policy returned %d: %s", response.Code, response.Body.String())
	}
}

func TestVerifyEndpointChecksStoredObject(t *testing.T) {
	root := t.TempDir()
	initializeLFSRepository(t, root)
	content := []byte("verified LFS object")
	sum := sha256.Sum256(content)
	oid := hex.EncodeToString(sum[:])
	handler := Handler{
		Storage: storage.Store{Root: root},
		Authorize: func(*http.Request, repopath.Repository, bool) (bool, bool) {
			return true, true
		},
	}
	upload := httptest.NewRecorder()
	handler.ServeHTTP(
		upload,
		httptest.NewRequest(
			http.MethodPut,
			"/g/r.git/info/lfs/objects/"+oid,
			bytes.NewReader(content),
		),
	)
	if upload.Code != http.StatusOK {
		t.Fatalf("upload returned %d: %s", upload.Code, upload.Body.String())
	}

	verify := func(method, objectID string, size int64) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(verifyRequest{OID: objectID, Size: size})
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(
			response,
			httptest.NewRequest(
				method,
				"/g/r.git/info/lfs/objects/verify",
				bytes.NewReader(body),
			),
		)
		return response
	}

	if response := verify(http.MethodPost, oid, int64(len(content))); response.Code != http.StatusOK {
		t.Fatalf("valid verify returned %d: %s", response.Code, response.Body.String())
	}
	if response := verify(http.MethodPost, oid, int64(len(content)+1)); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("size mismatch returned %d: %s", response.Code, response.Body.String())
	}
	missingOID := string(bytes.Repeat([]byte{'0'}, 64))
	if response := verify(http.MethodPost, missingOID, 1); response.Code != http.StatusNotFound {
		t.Fatalf("missing object returned %d: %s", response.Code, response.Body.String())
	}
	if response := verify(http.MethodPost, "invalid", 1); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid object returned %d: %s", response.Code, response.Body.String())
	}
	if response := verify(http.MethodGet, oid, int64(len(content))); response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("wrong method returned %d: %s", response.Code, response.Body.String())
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(
		response,
		httptest.NewRequest(
			http.MethodPost,
			"/g/r.git/info/lfs/objects/verify",
			bytes.NewBufferString(`{"oid":"`+oid+`","size":19} {}`),
		),
	)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("trailing JSON returned %d: %s", response.Code, response.Body.String())
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
	if err = VerifyObject(store, repository, oid, int64(len(content))); err != nil {
		t.Fatalf("VerifyObject() returned %v", err)
	}
	if err = VerifyObject(store, repository, oid, int64(len(content)+1)); !errors.Is(err, ErrObjectSizeMismatch) {
		t.Fatalf("size mismatch returned %v", err)
	}
}
