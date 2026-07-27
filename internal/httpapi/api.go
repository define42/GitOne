package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"example.com/puregit-server/internal/repopath"
	"example.com/puregit-server/internal/storage"
)

type API struct {
	Storage   storage.Store
	Authorize func(*http.Request, string, bool) bool
}
type createGroup struct {
	Path      string `json:"path"`
	Owner     string `json:"owner"`
	TokenName string `json:"tokenName"`
	Token     string `json:"token"`
}
type createRepo struct {
	Group string `json:"group"`
	Name  string `json:"name"`
}
type renameGroup struct {
	NewPath string `json:"newPath"`
}
type renameRepo struct {
	NewName string `json:"newName"`
}

func (a API) Routes() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	m.HandleFunc("POST /api/groups", a.createGroup)
	m.HandleFunc("DELETE /api/groups/{path...}", a.deleteGroup)
	m.HandleFunc("PATCH /api/groups/{path...}", a.renameGroup)
	m.HandleFunc("POST /api/repositories", a.createRepo)
	m.HandleFunc("DELETE /api/repositories/{path...}", a.deleteRepo)
	m.HandleFunc("PATCH /api/repositories/{path...}", a.renameRepo)
	return m
}
func (a API) allowed(r *http.Request, g string) bool {
	return a.Authorize == nil || a.Authorize(r, g, true)
}
func (a API) createGroup(w http.ResponseWriter, r *http.Request) {
	var q createGroup
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&q) != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	if !a.allowed(r, q.Path) {
		http.Error(w, "forbidden", 403)
		return
	}
	if _, e := repopath.ParseGroup(q.Path); e != nil {
		http.Error(w, e.Error(), 400)
		return
	}
	if q.Owner == "" {
		q.Owner = q.TokenName
	}
	sum := sha256.Sum256([]byte(q.Token))
	hash := "sha256:" + hex.EncodeToString(sum[:])
	if e := a.Storage.CreateGroup(q.Path, q.Owner, q.TokenName, hash); e != nil {
		http.Error(w, e.Error(), 409)
		return
	}
	w.WriteHeader(201)
}
func (a API) deleteGroup(w http.ResponseWriter, r *http.Request) {
	g := r.PathValue("path")
	if !a.allowed(r, g) {
		http.Error(w, "forbidden", 403)
		return
	}
	if e := a.Storage.DeleteGroup(g); e != nil {
		http.Error(w, e.Error(), 409)
		return
	}
	w.WriteHeader(204)
}
func (a API) renameGroup(w http.ResponseWriter, r *http.Request) {
	g := r.PathValue("path")
	if !a.allowed(r, g) {
		http.Error(w, "forbidden", 403)
		return
	}
	var q renameGroup
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&q) != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	if e := a.Storage.RenameGroup(g, q.NewPath); e != nil {
		http.Error(w, e.Error(), 409)
		return
	}
	w.WriteHeader(204)
}
func (a API) createRepo(w http.ResponseWriter, r *http.Request) {
	var q createRepo
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&q) != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	if !a.allowed(r, q.Group) {
		http.Error(w, "forbidden", 403)
		return
	}
	groups, e := repopath.ParseGroup(q.Group)
	if e != nil {
		http.Error(w, e.Error(), 400)
		return
	}
	repo := repopath.Repository{Groups: groups, Name: strings.TrimSuffix(q.Name, ".git")}
	if e = a.Storage.CreateRepository(repo); e != nil {
		http.Error(w, e.Error(), 409)
		return
	}
	w.WriteHeader(201)
}
func parseRepoPath(v string) (repopath.Repository, error) {
	return func() (repopath.Repository, error) {
		r, _, e := repopath.ParseGitRequestPath("/" + strings.TrimSuffix(v, ".git") + ".git/info/refs")
		return r, e
	}()
}
func (a API) deleteRepo(w http.ResponseWriter, r *http.Request) {
	repo, e := parseRepoPath(r.PathValue("path"))
	if e != nil {
		http.Error(w, e.Error(), 400)
		return
	}
	if !a.allowed(r, repo.Group()) {
		http.Error(w, "forbidden", 403)
		return
	}
	if e = a.Storage.DeleteRepository(repo); e != nil {
		http.Error(w, e.Error(), 409)
		return
	}
	w.WriteHeader(204)
}
func (a API) renameRepo(w http.ResponseWriter, r *http.Request) {
	repo, e := parseRepoPath(r.PathValue("path"))
	if e != nil {
		http.Error(w, e.Error(), 400)
		return
	}
	if !a.allowed(r, repo.Group()) {
		http.Error(w, "forbidden", 403)
		return
	}
	var q renameRepo
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&q) != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	if e = a.Storage.RenameRepository(repo, q.NewName); e != nil {
		http.Error(w, e.Error(), 409)
		return
	}
	w.WriteHeader(204)
}
