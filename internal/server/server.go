package server

import (
	"log"
	"net/http"
	"sync"

	"github.com/define42/GitOne/internal/auth"
	"github.com/define42/GitOne/internal/control"
	"github.com/define42/GitOne/internal/githttp"
	"github.com/define42/GitOne/internal/httpapi"
	"github.com/define42/GitOne/internal/lfs"
	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/runner"
	"github.com/define42/GitOne/internal/storage"
	"github.com/define42/GitOne/internal/webui"
)

type Config struct {
	Root, PublicURL string
	Directory       auth.IdentityProvider
	Sessions        *auth.SessionManager
	Runner          *runner.Runner
}

func New(c Config) http.Handler {
	st := storage.Store{Root: c.Root}
	cs := control.NewStore(c.Root)
	ar := &auth.Resolver{
		Controls:  cs,
		Directory: c.Directory,
	}
	sessions := c.Sessions
	if sessions == nil {
		var err error
		sessions, err = auth.NewEphemeralSessionManager(false)
		if err != nil {
			panic(err)
		}
	}
	authorizeRepo := func(r *http.Request, repo repopath.Repository, write bool) (bool, bool) {
		document, _ := cs.Load(r.Context(), repo.Group())
		if !write {
			visibility := document.Repositories[repo.Name].Visibility
			if visibility == "public" {
				return true, true
			}
			if visibility == "internal" {
				u, p, ok := r.BasicAuth()
				if !ok {
					return false, false
				}
				if _, authErr := ar.AuthenticateIdentity(r.Context(), u, p); authErr == nil {
					return true, true
				}
			}
		}
		u, p, ok := r.BasicAuth()
		if !ok {
			return false, false
		}
		pr, e := ar.Authenticate(r.Context(), repo.Group(), u, p)
		if e != nil {
			return false, false
		}
		need := control.RoleRead
		if write {
			need = control.RoleWrite
		}
		if repo.Name == "control" && write {
			need = control.RoleOwner
		}
		return true, pr.Role.Allows(need) && pr.AllowsRepository(repo.Name)
	}
	mux := http.NewServeMux()
	buildStore := runner.NewStore(c.Root)
	if c.Runner != nil {
		buildStore = c.Runner.Store()
	}
	httpapi.Register(mux, httpapi.API{
		Storage: st, Resolver: ar, Sessions: sessions, Builds: &buildStore, Runner: c.Runner,
	})
	ui := webui.Handler{}
	mux.Handle("GET /{$}", ui)
	mux.Handle("GET /groups/{path...}", ui)
	mux.Handle("GET /repositories/{path...}", ui)
	mux.Handle("GET /assets/{path...}", ui)
	lh := lfs.Handler{
		Storage:   st,
		PublicURL: c.PublicURL,
		Authorize: authorizeRepo,
		Policy: func(r *http.Request, repo repopath.Repository) (control.RepositoryPolicy, error) {
			document, err := cs.Load(r.Context(), repo.Group())
			if err != nil {
				return control.RepositoryPolicy{}, err
			}
			policy, configured := document.Repositories[repo.Name]
			if !configured {
				policy.LFS.Enabled = true
			}
			return policy, nil
		},
		UploadMu: &sync.Mutex{},
	}
	gh := githttp.Handler{
		Storage:        st,
		Authorize:      authorizeRepo,
		ReceiveMu:      &sync.Mutex{},
		ControlUpdated: cs.Invalidate,
	}
	if c.Runner != nil {
		gh.RepositoryUpdated = func(
			repository repopath.Repository,
			updates []githttp.ReferenceUpdate,
		) {
			for _, update := range updates {
				if _, err := c.Runner.Schedule(repository, update.Branch, update.Commit); err != nil {
					log.Printf(
						"could not schedule build for %s@%s: %v",
						repository.Full(),
						update.Commit,
						err,
					)
				}
			}
		}
	}
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
