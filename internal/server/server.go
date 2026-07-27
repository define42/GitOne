package server

import (
	"github.com/define42/GitOne/internal/auth"
	"github.com/define42/GitOne/internal/control"
	"github.com/define42/GitOne/internal/githttp"
	"github.com/define42/GitOne/internal/httpapi"
	"github.com/define42/GitOne/internal/lfs"
	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/storage"
	"github.com/define42/GitOne/internal/webui"
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
	authorizeGroup := func(r *http.Request, group string, write bool) (string, bool) {
		u, p, ok := r.BasicAuth()
		if !ok {
			return "", false
		}
		pr, e := ar.Authenticate(r.Context(), group, u, p)
		if e != nil {
			return "", false
		}
		need := control.RoleRead
		if write {
			need = control.RoleAdmin
		}
		if !pr.Role.Allows(need) {
			return "", false
		}
		return pr.Name, true
	}
	mux := http.NewServeMux()
	api := httpapi.API{Storage: st, Authorize: authorizeGroup}
	mux.Handle("/api/", api.Routes())
	mux.Handle("/healthz", api.Routes())
	ui := webui.Handler{Storage: st, Authorize: authorizeGroup}
	mux.Handle("GET /{$}", ui)
	mux.Handle("GET /groups/{path...}", ui)
	mux.Handle("POST /ui/groups", ui)
	mux.Handle("POST /ui/subgroups", ui)
	mux.Handle("POST /ui/repositories", ui)
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
