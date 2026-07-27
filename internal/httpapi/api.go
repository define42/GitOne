package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/storage"
)

type API struct {
	Storage   storage.Store
	Authorize func(*http.Request, string, bool) (string, bool)
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
	if a.Authorize == nil {
		return true
	}
	_, ok := a.Authorize(r, g, true)
	return ok
}
func (a API) createGroup(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseForm(); err != nil || len(r.PostForm) != 1 || len(r.PostForm["path"]) != 1 {
		http.Error(w, "invalid form", 400)
		return
	}
	group := r.PostForm.Get("path")
	if a.Authorize == nil {
		http.Error(w, "forbidden", 403)
		return
	}
	owner, ok := a.Authorize(r, group, true)
	if !ok || owner == "" {
		http.Error(w, "forbidden", 403)
		return
	}
	if _, e := repopath.ParseGroup(group); e != nil {
		http.Error(w, e.Error(), 400)
		return
	}
	if e := a.Storage.CreateGroup(group, owner); e != nil {
		http.Error(w, e.Error(), 409)
		return
	}
	w.WriteHeader(http.StatusCreated)
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
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseForm(); err != nil ||
		len(r.PostForm) != 2 ||
		len(r.PostForm["group"]) != 1 ||
		len(r.PostForm["name"]) != 1 {
		http.Error(w, "invalid form", 400)
		return
	}
	group := r.PostForm.Get("group")
	name := r.PostForm.Get("name")
	if !a.allowed(r, group) {
		http.Error(w, "forbidden", 403)
		return
	}
	groups, e := repopath.ParseGroup(group)
	if e != nil {
		http.Error(w, e.Error(), 400)
		return
	}
	repo := repopath.Repository{Groups: groups, Name: strings.TrimSuffix(name, ".git")}
	if e = a.Storage.CreateRepository(repo); e != nil {
		http.Error(w, e.Error(), 409)
		return
	}
	w.WriteHeader(http.StatusCreated)
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
