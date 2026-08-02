package lfs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/define42/GitOne/internal/auth"
	"github.com/define42/GitOne/internal/control"
	"github.com/define42/GitOne/internal/httpio"
	"github.com/define42/GitOne/internal/lockmgr"
	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/storage"
	git "github.com/go-git/go-git/v5"
)

const (
	maximumUploadBytes   int64 = 100 << 30
	maximumMetadataBytes int64 = 1 << 20
)

var (
	ErrInvalidObject      = errors.New("invalid LFS object")
	ErrObjectSizeMismatch = errors.New("LFS object size mismatch")
	ErrObjectStorage      = errors.New("LFS object storage error")
)

type uploadReservations struct {
	mutex sync.Mutex
	bytes map[string]int64
}

// processUploadReservations coordinates quota admission for uploads that
// release the group LFS lock while streaming request bodies.
//
//nolint:gochecknoglobals // GitOne intentionally coordinates uploads process-wide.
var processUploadReservations = uploadReservations{bytes: map[string]int64{}}

type Handler struct {
	Storage   storage.Store
	PublicURL string
	Authorize func(*http.Request, repopath.Repository, bool) (authenticated, allowed bool)
	Policy    func(*http.Request, repopath.Repository) (control.LFSPolicy, error)
}
type batchRequest struct {
	Operation string   `json:"operation"`
	Transfers []string `json:"transfers,omitempty"`
	Objects   []object `json:"objects"`
}
type object struct {
	OID     string            `json:"oid"`
	Size    int64             `json:"size"`
	Actions map[string]action `json:"actions,omitempty"`
	Error   *objError         `json:"error,omitempty"`
}
type action struct {
	Href   string            `json:"href"`
	Header map[string]string `json:"header,omitempty"`
}
type objError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
type batchResponse struct {
	Transfer string   `json:"transfer"`
	Objects  []object `json:"objects"`
}
type verifyRequest struct {
	OID  string `json:"oid"`
	Size int64  `json:"size"`
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	repo, suffix, e := repopath.ParseGitRequestPath(r.URL.Path)
	maximumBodyBytes := int64(0)
	if e == nil {
		switch {
		case suffix == "/info/lfs/objects/batch",
			suffix == "/info/lfs/objects/verify":
			maximumBodyBytes = maximumMetadataBytes
		case strings.HasPrefix(suffix, "/info/lfs/objects/") && r.Method == http.MethodPut:
			maximumBodyBytes = maximumUploadBytes
		}
	}
	w, cleanup := httpio.Protect(
		w,
		r,
		httpio.DefaultIdleTimeout,
		maximumBodyBytes,
	)
	defer cleanup()
	if e != nil {
		http.Error(w, e.Error(), http.StatusBadRequest)
		return
	}
	if r.ContentLength > maximumBodyBytes {
		http.Error(w, "LFS request body is too large", http.StatusRequestEntityTooLarge)
		return
	}
	switch {
	case suffix == "/info/lfs/objects/batch":
		h.batch(w, r, repo)
	case suffix == "/info/lfs/objects/verify":
		h.verify(w, r, repo)
	case strings.HasPrefix(suffix, "/info/lfs/objects/"):
		if !h.authorize(w, r, repo, r.Method == http.MethodPut) {
			return
		}
		_, ok := h.policy(w, r, repo)
		if !ok {
			return
		}
		h.object(w, r, repo, strings.TrimPrefix(suffix, "/info/lfs/objects/"))
	default:
		http.NotFound(w, r)
	}
}

func (h Handler) verify(w http.ResponseWriter, r *http.Request, repo repopath.Repository) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !h.authorize(w, r, repo, true) {
		return
	}
	if _, ok := h.policy(w, r, repo); !ok {
		return
	}

	var q verifyRequest
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&q); err != nil {
		if httpio.BodyTooLarge(err) {
			http.Error(w, "LFS verify request is too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if httpio.BodyTooLarge(err) {
			http.Error(w, "LFS verify request is too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if err := VerifyObject(h.Storage, repo, q.OID, q.Size); err != nil {
		switch {
		case errors.Is(err, os.ErrNotExist):
			http.Error(w, "object not found", http.StatusNotFound)
		case errors.Is(err, ErrInvalidObject), errors.Is(err, ErrObjectSizeMismatch):
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		default:
			http.Error(w, "could not inspect LFS object", http.StatusInternalServerError)
		}
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h Handler) batch(w http.ResponseWriter, r *http.Request, repo repopath.Repository) {
	var q batchRequest
	d := json.NewDecoder(r.Body)
	if err := d.Decode(&q); err != nil {
		if httpio.BodyTooLarge(err) {
			http.Error(w, "LFS batch request is too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid JSON", 400)
		return
	}
	var trailing any
	if err := d.Decode(&trailing); !errors.Is(err, io.EOF) {
		if httpio.BodyTooLarge(err) {
			http.Error(w, "LFS batch request is too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if !h.authorize(w, r, repo, q.Operation == "upload") {
		return
	}
	if q.Operation != "upload" && q.Operation != "download" {
		http.Error(w, "unsupported LFS operation", http.StatusUnprocessableEntity)
		return
	}
	policy, ok := h.policy(w, r, repo)
	if !ok {
		return
	}
	resp := batchResponse{Transfer: "basic"}
	var storageBytes int64
	if q.Operation == "upload" && policy.MaximumStorageBytes > 0 {
		var usageErr error
		storageBytes, usageErr = h.groupStorageUsage(repo.Group())
		if usageErr != nil {
			http.Error(w, "could not inspect LFS storage", http.StatusInternalServerError)
			return
		}
	}
	pending := map[string]int64{}
	for _, o := range q.Objects {
		if !validOID(o.OID) || o.Size < 0 {
			o.Error = &objError{422, "invalid object"}
			resp.Objects = append(resp.Objects, o)
			continue
		}
		p, _ := h.objectPath(repo, o.OID)
		info, e := os.Stat(p)
		base := strings.TrimRight(h.PublicURL, "/") + "/" + repo.Full() + ".git/info/lfs/objects/" + o.OID
		verifyURL := strings.TrimRight(h.PublicURL, "/") + "/" + repo.Full() + ".git/info/lfs/objects/verify"
		o.Actions = map[string]action{}
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			o.Error = &objError{500, "could not inspect object"}
			o.Actions = nil
			resp.Objects = append(resp.Objects, o)
			continue
		}
		if e == nil && (!info.Mode().IsRegular() || info.Size() != o.Size) {
			o.Error = &objError{422, "stored object size does not match requested size"}
			o.Actions = nil
			resp.Objects = append(resp.Objects, o)
			continue
		}
		if q.Operation == "upload" && errors.Is(e, os.ErrNotExist) {
			additionalBytes := o.Size
			if _, exists := pending[o.OID]; exists {
				additionalBytes = 0
			}
			switch {
			case policy.MaximumObjectBytes > 0 && o.Size > policy.MaximumObjectBytes:
				o.Error = &objError{422, "object exceeds the group LFS object limit"}
			case policy.MaximumStorageBytes > 0 && o.Size > policy.MaximumStorageBytes:
				// A single object can never fit within the total storage
				// quota; short-circuiting here also prevents the running sum
				// below from overflowing on an attacker-supplied huge size.
				o.Error = &objError{422, "object exceeds the group LFS storage limit"}
			case policy.MaximumStorageBytes > 0 &&
				storageBytes+pendingBytes(pending)+additionalBytes > policy.MaximumStorageBytes:
				o.Error = &objError{422, "object exceeds the group LFS storage limit"}
			default:
				o.Actions["upload"] = action{Href: base}
				o.Actions["verify"] = action{Href: verifyURL}
				pending[o.OID] = o.Size
			}
		}
		if q.Operation == "download" {
			if e == nil {
				o.Actions["download"] = action{Href: base}
			} else {
				o.Error = &objError{404, "object not found"}
			}
		}
		if len(o.Actions) == 0 {
			o.Actions = nil
		}
		resp.Objects = append(resp.Objects, o)
	}
	w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
	_ = json.NewEncoder(w).Encode(resp)
}

func pendingBytes(objects map[string]int64) int64 {
	var total int64
	for _, size := range objects {
		total += size
	}
	return total
}

func (h Handler) authorize(w http.ResponseWriter, r *http.Request, repo repopath.Repository, write bool) bool {
	if h.Authorize == nil {
		return true
	}
	authenticated, allowed := h.Authorize(r, repo, write)
	if allowed {
		return true
	}
	if retryAfter, limited := auth.RequestRateLimit(r.Context()); limited {
		seconds := max(1, int((retryAfter+time.Second-1)/time.Second))
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		http.Error(w, "too many authentication attempts", http.StatusTooManyRequests)
		return false
	}
	if !authenticated {
		w.Header().Set("WWW-Authenticate", `Basic realm="GitOne"`)
		http.Error(w, "authentication required", http.StatusUnauthorized)
	} else {
		http.Error(w, "forbidden", http.StatusForbidden)
	}
	return false
}

func (h Handler) policy(
	w http.ResponseWriter,
	r *http.Request,
	repo repopath.Repository,
) (control.LFSPolicy, bool) {
	if h.Policy == nil {
		return control.LFSPolicy{Enabled: true}, true
	}
	policy, err := h.Policy(r, repo)
	if err != nil {
		http.Error(w, "could not load group LFS policy", http.StatusInternalServerError)
		return control.LFSPolicy{}, false
	}
	if !policy.Enabled {
		http.Error(w, "Git LFS is disabled for this group", http.StatusForbidden)
		return control.LFSPolicy{}, false
	}
	return policy, true
}

func (h Handler) object(
	w http.ResponseWriter,
	r *http.Request,
	repo repopath.Repository,
	oid string,
) {
	if !validOID(oid) {
		http.Error(w, "invalid oid", 400)
		return
	}
	switch r.Method {
	case http.MethodPut:
		policy, ok := h.policy(w, r, repo)
		if !ok {
			return
		}
		if policy.MaximumObjectBytes > 0 &&
			r.ContentLength > policy.MaximumObjectBytes {
			http.Error(w, "object exceeds the group LFS object limit", http.StatusUnprocessableEntity)
			return
		}
		if policy.MaximumStorageBytes > 0 &&
			r.ContentLength > policy.MaximumStorageBytes {
			http.Error(w, "object exceeds the group LFS storage limit", http.StatusUnprocessableEntity)
			return
		}
		releaseAdmission, err := lockmgr.Process.Acquire(
			lockmgr.LFSRequests(h.Storage.Root, repo.Group())...,
		)
		if err != nil {
			http.Error(w, "could not lock LFS quota admission", http.StatusInternalServerError)
			return
		}
		policy, ok = h.policy(w, r, repo)
		if !ok {
			releaseAdmission()
			return
		}
		stageLimit := maximumUploadBytes
		if policy.MaximumObjectBytes > 0 && policy.MaximumObjectBytes < stageLimit {
			stageLimit = policy.MaximumObjectBytes
		}
		if r.ContentLength >= 0 && r.ContentLength > stageLimit {
			releaseAdmission()
			http.Error(w, "object exceeds the group LFS object limit", http.StatusUnprocessableEntity)
			return
		}
		limitError := errors.New("object exceeds the group LFS object limit")
		releaseReservation := func() {}
		if policy.MaximumStorageBytes > 0 {
			usage, usageErr := h.groupStorageUsage(repo.Group())
			if usageErr != nil {
				releaseAdmission()
				http.Error(w, "could not inspect LFS storage", http.StatusInternalServerError)
				return
			}
			groupRoot, pathErr := h.Storage.GroupPath(repo.Group())
			if pathErr != nil {
				releaseAdmission()
				http.Error(w, "bad path", http.StatusBadRequest)
				return
			}
			var reservationErr error
			stageLimit, releaseReservation, reservationErr = processUploadReservations.reserve(
				groupRoot,
				usage,
				policy.MaximumStorageBytes,
				stageLimit,
				r.ContentLength,
			)
			if reservationErr != nil {
				releaseAdmission()
				http.Error(w, reservationErr.Error(), http.StatusUnprocessableEntity)
				return
			}
			if stageLimit < maximumUploadBytes &&
				(policy.MaximumObjectBytes == 0 || stageLimit < policy.MaximumObjectBytes) {
				limitError = errors.New("object exceeds the group LFS storage limit")
			}
		}
		releaseAdmission()
		defer releaseReservation()

		stagedPath, stagedSize, err := h.stageUpload(r, oid, stageLimit, limitError)
		if err != nil {
			if httpio.BodyTooLarge(err) {
				http.Error(w, "LFS object is too large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		defer func() {
			_ = os.Remove(stagedPath)
		}()

		requests := lockmgr.RepositoryRequests(
			h.Storage.Root,
			[]repopath.Repository{repo},
			lockmgr.Shared,
		)
		requests = append(requests, lockmgr.LFSRequests(h.Storage.Root, repo.Group())...)
		releaseOperation, err := lockmgr.Process.Acquire(requests...)
		if err != nil {
			http.Error(w, "could not lock repository operations", http.StatusInternalServerError)
			return
		}
		defer func() {
			releaseOperation()
		}()
		if !h.authorize(w, r, repo, true) {
			return
		}
		policy, ok = h.policy(w, r, repo)
		if !ok {
			return
		}
		gitPath, err := h.Storage.GitPath(repo)
		if err != nil {
			http.Error(w, "bad path", http.StatusBadRequest)
			return
		}
		if _, err = git.PlainOpen(gitPath); err != nil {
			http.NotFound(w, r)
			return
		}
		p, err := h.objectPath(repo, oid)
		if err != nil {
			http.Error(w, "bad path", http.StatusBadRequest)
			return
		}
		if policy.MaximumObjectBytes > 0 &&
			stagedSize > policy.MaximumObjectBytes {
			http.Error(w, "object exceeds the group LFS object limit", http.StatusUnprocessableEntity)
			return
		}
		if err = h.publishUpload(repo, p, stagedPath, stagedSize, policy); err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		// Atomically transition from reserved staging bytes to published bytes
		// while the group LFS lock is still held.
		releaseReservation()
		w.WriteHeader(200)
	case http.MethodGet, http.MethodHead:
		p, err := h.objectPath(repo, oid)
		if err != nil {
			http.Error(w, "bad path", http.StatusBadRequest)
			return
		}
		f, err := os.Open(p)
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, "storage error", 500)
			return
		}
		defer func() {
			_ = f.Close()
		}()
		st, statErr := f.Stat()
		if statErr != nil {
			http.Error(w, "storage error", 500)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
		w.Header().Set("ETag", `"`+oid+`"`)
		if r.Method == http.MethodGet {
			http.ServeContent(w, r, oid, st.ModTime(), f)
		}
	default:
		w.WriteHeader(405)
	}
}

func (h Handler) stageUpload(
	r *http.Request,
	oid string,
	limit int64,
	limitError error,
) (string, int64, error) {
	root, err := repopath.SafeJoin(h.Storage.Root, ".gitone", "uploads")
	if err != nil {
		return "", 0, err
	}
	if err = os.MkdirAll(root, 0o750); err != nil {
		return "", 0, err
	}
	temporary, err := os.CreateTemp(root, "object-*")
	if err != nil {
		return "", 0, err
	}
	name := temporary.Name()
	defer func() {
		_ = temporary.Close()
	}()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(r.Body, limit))
	if err != nil {
		_ = temporary.Close()
		_ = os.Remove(name)
		return "", 0, err
	}
	extra, err := io.ReadAll(io.LimitReader(r.Body, 1))
	if err != nil {
		_ = temporary.Close()
		_ = os.Remove(name)
		return "", 0, err
	}
	if len(extra) != 0 {
		_ = temporary.Close()
		_ = os.Remove(name)
		return "", 0, limitError
	}
	if err = temporary.Sync(); err != nil {
		_ = temporary.Close()
		_ = os.Remove(name)
		return "", 0, err
	}
	if err = temporary.Close(); err != nil {
		_ = os.Remove(name)
		return "", 0, err
	}
	if hex.EncodeToString(hash.Sum(nil)) != oid {
		_ = os.Remove(name)
		return "", 0, errors.New("sha256 mismatch")
	}
	return name, n, nil
}

func (r *uploadReservations) reserve(
	key string,
	usage int64,
	quota int64,
	maximum int64,
	contentLength int64,
) (int64, func(), error) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	available := int64(0)
	if usage < quota {
		available = quota - usage
		if reserved := r.bytes[key]; reserved < available {
			available -= reserved
		} else {
			available = 0
		}
	}
	reservation := maximum
	if contentLength >= 0 {
		reservation = contentLength
		if reservation > available {
			return 0, func() {}, errors.New("object exceeds the group LFS storage limit")
		}
	} else if reservation > available {
		reservation = available
	}
	if reservation == 0 {
		return 0, func() {}, nil
	}
	r.bytes[key] += reservation
	var once sync.Once
	return reservation, func() {
		once.Do(func() {
			r.mutex.Lock()
			r.bytes[key] -= reservation
			if r.bytes[key] == 0 {
				delete(r.bytes, key)
			}
			r.mutex.Unlock()
		})
	}, nil
}

func (h Handler) publishUpload(
	repo repopath.Repository,
	p string,
	stagedPath string,
	stagedSize int64,
	policy control.LFSPolicy,
) error {
	if _, err := os.Stat(p); err == nil {
		return nil
	}
	if policy.MaximumStorageBytes > 0 {
		usage, usageErr := h.groupStorageUsage(repo.Group())
		if usageErr != nil {
			return usageErr
		}
		if usage+stagedSize > policy.MaximumStorageBytes {
			return errors.New("object exceeds the group LFS storage limit")
		}
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return err
	}
	if _, err := os.Stat(p); err == nil {
		return nil
	}
	return os.Rename(stagedPath, p)
}

func (h Handler) groupStorageUsage(group string) (int64, error) {
	root, err := h.Storage.GroupPath(group)
	if err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, repository := range entries {
		if !repository.IsDir() || !strings.HasSuffix(repository.Name(), ".lfs") {
			continue
		}
		objects := filepath.Join(root, repository.Name(), "objects")
		err = filepath.WalkDir(objects, func(_ string, entry os.DirEntry, walkErr error) error {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !validOID(entry.Name()) {
				return nil
			}
			info, infoErr := entry.Info()
			if infoErr != nil {
				return infoErr
			}
			total += info.Size()
			return nil
		})
		if err != nil {
			return 0, err
		}
	}
	return total, nil
}

func (h Handler) objectPath(repo repopath.Repository, oid string) (string, error) {
	return objectPath(h.Storage, repo, oid)
}

func OpenObject(store storage.Store, repo repopath.Repository, oid string) (*os.File, error) {
	if !validOID(oid) {
		return nil, ErrInvalidObject
	}
	path, err := objectPath(store, repo, oid)
	if err != nil {
		return nil, err
	}
	return os.Open(path)
}

func VerifyObject(
	store storage.Store,
	repo repopath.Repository,
	oid string,
	expectedSize int64,
) error {
	if !validOID(oid) || expectedSize < 0 {
		return ErrInvalidObject
	}
	file, err := OpenObject(store, repo, oid)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("LFS object %s not found: %w", oid, os.ErrNotExist)
	}
	if err != nil {
		return fmt.Errorf("%w: could not open object %s", ErrObjectStorage, oid)
	}
	defer func() {
		_ = file.Close()
	}()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("%w: could not inspect object %s", ErrObjectStorage, oid)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: stored object is not a regular file", ErrInvalidObject)
	}
	if info.Size() != expectedSize {
		return fmt.Errorf(
			"%w: expected %d bytes, found %d",
			ErrObjectSizeMismatch,
			expectedSize,
			info.Size(),
		)
	}
	return nil
}

func objectPath(store storage.Store, repo repopath.Repository, oid string) (string, error) {
	root, e := store.LFSPath(repo)
	if e != nil {
		return "", e
	}
	return repopath.SafeJoin(root, "objects", oid[:2], oid[2:4], oid)
}

func validOID(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, e := hex.DecodeString(s)
	return e == nil && strings.ToLower(s) == s
}
