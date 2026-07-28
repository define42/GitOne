package lfs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/storage"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Handler struct {
	Storage   storage.Store
	PublicURL string
	Authorize func(*http.Request, repopath.Repository, bool) (authenticated, allowed bool)
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
	case strings.HasPrefix(suffix, "/info/lfs/objects/"):
		if !h.authorize(w, r, repo, r.Method == http.MethodPut) {
			return
		}
		h.object(w, r, repo, strings.TrimPrefix(suffix, "/info/lfs/objects/"))
	case suffix == "/info/lfs/objects/verify":
		if !h.authorize(w, r, repo, true) {
			return
		}
		w.WriteHeader(200)
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
	resp := batchResponse{Transfer: "basic"}
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
			o.Actions["upload"] = action{Href: base}
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
	json.NewEncoder(w).Encode(resp)
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

func (h Handler) object(w http.ResponseWriter, r *http.Request, repo repopath.Repository, oid string) {
	if !validOID(oid) {
		http.Error(w, "invalid oid", 400)
		return
	}
	p, e := h.objectPath(repo, oid)
	if e != nil {
		http.Error(w, "bad path", 400)
		return
	}
	switch r.Method {
	case http.MethodPut:
		if e = h.upload(r, p, oid); e != nil {
			http.Error(w, e.Error(), 422)
			return
		}
		w.WriteHeader(200)
	case http.MethodGet, http.MethodHead:
		f, e := os.Open(p)
		if errors.Is(e, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		if e != nil {
			http.Error(w, "storage error", 500)
			return
		}
		defer f.Close()
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
func (h Handler) upload(r *http.Request, p, oid string) error {
	if e := os.MkdirAll(filepath.Dir(p), 0750); e != nil {
		return e
	}
	tmp, e := os.CreateTemp(filepath.Dir(p), ".upload-")
	if e != nil {
		return e
	}
	name := tmp.Name()
	defer os.Remove(name)
	hash := sha256.New()
	n, e := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(r.Body, 100<<30))
	if e != nil {
		tmp.Close()
		return e
	}
	_ = n
	if e = tmp.Sync(); e != nil {
		tmp.Close()
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
	return os.Rename(name, p)
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

var _ = fmt.Sprintf
