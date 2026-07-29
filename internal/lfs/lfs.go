package lfs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/define42/GitOne/internal/control"
	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/review"
	"github.com/define42/GitOne/internal/storage"
	git "github.com/go-git/go-git/v5"
)

const maximumUploadBytes int64 = 100 << 30

type Handler struct {
	Storage   storage.Store
	PublicURL string
	Authorize func(*http.Request, repopath.Repository, bool) (authenticated, allowed bool)
	Policy    func(*http.Request, repopath.Repository) (control.RepositoryPolicy, error)
	UploadMu  *sync.Mutex
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

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	repo, suffix, e := repopath.ParseGitRequestPath(r.URL.Path)
	if e != nil {
		http.Error(w, e.Error(), 400)
		return
	}
	switch {
	case suffix == "/info/lfs/objects/batch":
		h.batch(w, r, repo)
	case suffix == "/info/lfs/objects/verify":
		if !h.authorize(w, r, repo, true) {
			return
		}
		if _, ok := h.policy(w, r, repo); !ok {
			return
		}
		w.WriteHeader(200)
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

func (h Handler) batch(w http.ResponseWriter, r *http.Request, repo repopath.Repository) {
	var q batchRequest
	d := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if d.Decode(&q) != nil {
		http.Error(w, "invalid JSON", 400)
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
	if q.Operation == "upload" && policy.LFS.MaximumStorageBytes > 0 {
		var usageErr error
		storageBytes, usageErr = h.storageUsage(repo)
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
		_, e := os.Stat(p)
		base := strings.TrimRight(h.PublicURL, "/") + "/" + repo.Full() + ".git/info/lfs/objects/" + o.OID
		o.Actions = map[string]action{}
		if q.Operation == "upload" && errors.Is(e, os.ErrNotExist) {
			additionalBytes := o.Size
			if _, exists := pending[o.OID]; exists {
				additionalBytes = 0
			}
			switch {
			case policy.LFS.MaximumObjectBytes > 0 && o.Size > policy.LFS.MaximumObjectBytes:
				o.Error = &objError{422, "object exceeds the repository LFS object limit"}
			case policy.LFS.MaximumStorageBytes > 0 &&
				storageBytes+pendingBytes(pending)+additionalBytes > policy.LFS.MaximumStorageBytes:
				o.Error = &objError{422, "object exceeds the repository LFS storage limit"}
			default:
				o.Actions["upload"] = action{Href: base}
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
) (control.RepositoryPolicy, bool) {
	if h.Policy == nil {
		return control.RepositoryPolicy{LFS: control.LFSPolicy{Enabled: true}}, true
	}
	policy, err := h.Policy(r, repo)
	if err != nil {
		http.Error(w, "could not load repository LFS policy", http.StatusInternalServerError)
		return control.RepositoryPolicy{}, false
	}
	if !policy.LFS.Enabled {
		http.Error(w, "Git LFS is disabled for this repository", http.StatusForbidden)
		return control.RepositoryPolicy{}, false
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
		releaseOperation, err := review.NewStore(h.Storage.Root).AcquireOperationLock()
		if err != nil {
			http.Error(w, "could not lock repository operations", http.StatusInternalServerError)
			return
		}
		defer func() {
			_ = releaseOperation()
		}()
		if !h.authorize(w, r, repo, true) {
			return
		}
		policy, ok := h.policy(w, r, repo)
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
		if policy.LFS.MaximumObjectBytes > 0 &&
			r.ContentLength > policy.LFS.MaximumObjectBytes {
			http.Error(w, "object exceeds the repository LFS object limit", http.StatusUnprocessableEntity)
			return
		}
		if err = h.upload(r, repo, p, oid, policy.LFS); err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
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
		st, _ := f.Stat()
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

func (h Handler) upload(
	r *http.Request,
	repo repopath.Repository,
	p string,
	oid string,
	policy control.LFSPolicy,
) error {
	mu := h.UploadMu
	if mu == nil {
		mu = &fallbackUploadMu
	}
	mu.Lock()
	defer mu.Unlock()

	if _, err := os.Stat(p); err == nil {
		return nil
	}
	if e := os.MkdirAll(filepath.Dir(p), 0o750); e != nil {
		return e
	}
	tmp, e := os.CreateTemp(filepath.Dir(p), ".upload-")
	if e != nil {
		return e
	}
	name := tmp.Name()
	defer func() {
		_ = os.Remove(name)
	}()
	hash := sha256.New()
	limit := maximumUploadBytes
	if policy.MaximumObjectBytes > 0 && policy.MaximumObjectBytes < limit {
		limit = policy.MaximumObjectBytes
	}
	n, e := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(r.Body, limit+1))
	if e != nil {
		_ = tmp.Close()
		return e
	}
	if n > limit {
		_ = tmp.Close()
		return errors.New("object exceeds the repository LFS object limit")
	}
	if e = tmp.Sync(); e != nil {
		_ = tmp.Close()
		return e
	}
	if e = tmp.Close(); e != nil {
		return e
	}
	if hex.EncodeToString(hash.Sum(nil)) != oid {
		return errors.New("sha256 mismatch")
	}
	if _, e = os.Stat(p); e == nil {
		return nil
	}
	if policy.MaximumStorageBytes > 0 {
		usage, usageErr := h.storageUsage(repo)
		if usageErr != nil {
			return usageErr
		}
		if usage+n > policy.MaximumStorageBytes {
			return errors.New("object exceeds the repository LFS storage limit")
		}
	}
	return os.Rename(name, p)
}

func (h Handler) storageUsage(repo repopath.Repository) (int64, error) {
	root, err := h.Storage.LFSPath(repo)
	if err != nil {
		return 0, err
	}
	var total int64
	err = filepath.WalkDir(filepath.Join(root, "objects"), func(_ string, entry os.DirEntry, walkErr error) error {
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
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	return total, err
}

func (h Handler) objectPath(repo repopath.Repository, oid string) (string, error) {
	return objectPath(h.Storage, repo, oid)
}

func OpenObject(store storage.Store, repo repopath.Repository, oid string) (*os.File, error) {
	if !validOID(oid) {
		return nil, errors.New("invalid oid")
	}
	path, err := objectPath(store, repo, oid)
	if err != nil {
		return nil, err
	}
	return os.Open(path)
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

//nolint:gochecknoglobals // The fallback must serialize uploads across all handler instances.
var fallbackUploadMu sync.Mutex
