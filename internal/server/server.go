package server

import (
	"log"
	"net/http"
	"strings"

	"github.com/define42/GitOne/internal/auth"
	"github.com/define42/GitOne/internal/control"
	"github.com/define42/GitOne/internal/githttp"
	"github.com/define42/GitOne/internal/httpapi"
	"github.com/define42/GitOne/internal/lfs"
	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/review"
	"github.com/define42/GitOne/internal/runner"
	"github.com/define42/GitOne/internal/storage"
	"github.com/define42/GitOne/internal/webui"
)

type Config struct {
	Root, PublicURL     string
	Directory           auth.IdentityProvider
	Sessions            *auth.SessionManager
	Coordinator         *runner.Coordinator
	RunnerToken         string
	ImportNetworkPolicy storage.ImportNetworkPolicy
	AuthAttempts        *auth.AttemptLimiter
}

func New(c Config) http.Handler {
	st := storage.Store{Root: c.Root}
	cs := control.NewStore(c.Root)
	attempts := c.AuthAttempts
	if attempts == nil {
		attempts = auth.NewAttemptLimiter(auth.AttemptLimiterOptions{})
	}
	ar := &auth.Resolver{
		Controls:  cs,
		Directory: c.Directory,
		Attempts:  attempts,
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
		if !write && repo.Name != "control" {
			if document.Visibility == "public" {
				return true, true
			}
			if document.Visibility == "internal" {
				u, p, ok := r.BasicAuth()
				if !ok {
					return false, false
				}
				if _, authErr := ar.AuthenticateIdentity(r.Context(), u, p); authErr == nil {
					return true, true
				} else if authErr != nil {
					auth.MarkRequestRateLimited(r.Context(), authErr)
					if _, limited := auth.RequestRateLimit(r.Context()); limited {
						return false, false
					}
				}
			}
		}
		u, p, ok := r.BasicAuth()
		if !ok {
			return false, false
		}
		pr, e := ar.Authenticate(r.Context(), repo.Group(), u, p)
		if e != nil {
			auth.MarkRequestRateLimited(r.Context(), e)
			return false, false
		}
		need := control.RoleRead
		if write {
			need = control.RoleDeveloper
		}
		if repo.Name == "control" {
			// control.git stores the group's member roles and Argon2id token
			// hashes. Gate reads at the same level the settings API requires
			// (maintainer) and writes at owner, so a read- or developer-scoped
			// member cannot clone it to harvest hashes and enumerate members.
			need = control.RoleMaintainer
			if write {
				need = control.RoleOwner
			}
		}
		return true, pr.Role.Allows(need)
	}
	mux := http.NewServeMux()
	buildStore := runner.NewStore(c.Root)
	reviewStore := review.NewStore(c.Root)
	var scheduler runner.Scheduler
	if c.Coordinator != nil {
		buildStore = c.Coordinator.Store()
		scheduler = c.Coordinator
	}
	httpapi.Register(mux, httpapi.API{
		Storage:             st,
		Resolver:            ar,
		Sessions:            sessions,
		Builds:              &buildStore,
		Reviews:             reviewStore,
		Scheduler:           scheduler,
		Coordinator:         c.Coordinator,
		RunnerToken:         c.RunnerToken,
		ImportNetworkPolicy: c.ImportNetworkPolicy,
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
		Policy: func(r *http.Request, repo repopath.Repository) (control.LFSPolicy, error) {
			document, err := cs.Load(r.Context(), repo.Group())
			if err != nil {
				return control.LFSPolicy{}, err
			}
			return document.LFS, nil
		},
	}
	gh := githttp.Handler{
		Storage:        st,
		Authorize:      authorizeRepo,
		ControlUpdated: cs.Invalidate,
	}
	if scheduler != nil {
		gh.RepositoryUpdated = func(
			repository repopath.Repository,
			updates []githttp.ReferenceUpdate,
		) {
			schedule := scheduler.Schedule
			if locked, ok := scheduler.(runner.LockedScheduler); ok {
				schedule = locked.ScheduleLocked
			}
			for _, update := range updates {
				if _, err := schedule(repository, update.Branch, update.Commit); err != nil {
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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := auth.WithClientIP(r.Context(), r.RemoteAddr)
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}

func containsLFS(p string) bool {
	_, suffix, err := repopath.ParseGitRequestPath(p)
	return err == nil && strings.HasPrefix(suffix, "/info/lfs/")
}
