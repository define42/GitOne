package server

import (
	"example.com/puregit-server/internal/auth"
	"example.com/puregit-server/internal/control"
	"example.com/puregit-server/internal/githttp"
	"example.com/puregit-server/internal/httpapi"
	"example.com/puregit-server/internal/lfs"
	"example.com/puregit-server/internal/repopath"
	"example.com/puregit-server/internal/storage"
	"net/http"
)

type Config struct{ Root, PublicURL, BootstrapUser, BootstrapToken string }

func New(c Config) http.Handler {
	st := storage.Store{Root: c.Root}
	cs := control.NewStore(c.Root)
	ar := &auth.Resolver{Controls: cs, BootstrapUser: c.BootstrapUser, BootstrapToken: c.BootstrapToken}
	authorizeRepo := func(r *http.Request, repo repopath.Repository, write bool) bool {
		u, p, ok := r.BasicAuth()
		if !ok {
			return false
		}
		pr, e := ar.Authenticate(r.Context(), repo.Group(), u, p)
		if e != nil {
			return false
		}
		need := control.RoleRead
		if write {
			need = control.RoleWrite
		}
		if repo.Name == "control" && write {
			need = control.RoleOwner
		}
		return pr.Role.Allows(need)
	}
	authorizeGroup := func(r *http.Request, group string, write bool) bool {
		u, p, ok := r.BasicAuth()
		if !ok {
			return false
		}
		pr, e := ar.Authenticate(r.Context(), group, u, p)
		if e != nil {
			return false
		}
		need := control.RoleRead
		if write {
			need = control.RoleAdmin
		}
		return pr.Role.Allows(need)
	}
	mux := http.NewServeMux()
	api := httpapi.API{Storage: st, Authorize: authorizeGroup}
	mux.Handle("/api/", api.Routes())
	mux.Handle("/healthz", api.Routes())
	lh := lfs.Handler{Storage: st, PublicURL: c.PublicURL, Authorize: authorizeRepo}
	gh := githttp.Handler{Storage: st, Authorize: authorizeRepo}
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if containsLFS(r.URL.Path) {
			lh.ServeHTTP(w, r)
			return
		}
		gh.ServeHTTP(w, r)
	}))
	return mux
}
func containsLFS(p string) bool {
	for i := 0; i+9 <= len(p); i++ {
		if p[i:i+9] == "/info/lfs" {
			return true
		}
	}
	return false
}
