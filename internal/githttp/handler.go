package githttp

import (
	"fmt"
	"net/http"

	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/storage"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/pktline"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/transport"
	gitserver "github.com/go-git/go-git/v5/plumbing/transport/server"
)

type Authorizer func(*http.Request, repopath.Repository, bool) bool

type Handler struct {
	Storage   storage.Store
	Authorize Authorizer
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	repo, suffix, err := repopath.ParseGitRequestPath(r.URL.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	write := suffix == "/git-receive-pack" || (suffix == "/info/refs" && r.URL.Query().Get("service") == "git-receive-pack")
	if h.Authorize != nil && !h.Authorize(r, repo, write) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	switch {
	case suffix == "/info/refs":
		h.advertise(w, r, repo)
	case suffix == "/git-upload-pack" && r.Method == http.MethodPost:
		h.uploadPack(w, r, repo)
	case suffix == "/git-receive-pack" && r.Method == http.MethodPost:
		h.receivePack(w, r, repo)
	default:
		http.NotFound(w, r)
	}
}

func (h Handler) transport(repo repopath.Repository) (transport.Transport, *transport.Endpoint, error) {
	p, err := h.Storage.GitPath(repo)
	if err != nil {
		return nil, nil, err
	}
	loader := gitserver.NewFilesystemLoader(osfs.New("/"))
	ep, err := transport.NewEndpoint(p)
	if err != nil {
		return nil, nil, err
	}
	return gitserver.NewServer(loader), ep, nil
}

func (h Handler) advertise(w http.ResponseWriter, r *http.Request, repo repopath.Repository) {
	service := r.URL.Query().Get("service")
	if service != "git-upload-pack" && service != "git-receive-pack" {
		http.Error(w, "unsupported service", 400)
		return
	}
	t, ep, err := h.transport(repo)
	if err != nil {
		http.Error(w, "repository not found", 404)
		return
	}
	var adv *packp.AdvRefs
	if service == "git-upload-pack" {
		s, e := t.NewUploadPackSession(ep, nil)
		if e != nil {
			http.Error(w, e.Error(), 404)
			return
		}
		defer s.Close()
		adv, err = s.AdvertisedReferencesContext(r.Context())
	} else {
		s, e := t.NewReceivePackSession(ep, nil)
		if e != nil {
			http.Error(w, e.Error(), 404)
			return
		}
		defer s.Close()
		adv, err = s.AdvertisedReferencesContext(r.Context())
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/x-"+service+"-advertisement")
	w.Header().Set("Cache-Control", "no-cache")
	enc := pktline.NewEncoder(w)
	if err := enc.Encodef("# service=%s\n", service); err != nil {
		return
	}
	if err := enc.Flush(); err != nil {
		return
	}
	if err := adv.Encode(w); err != nil {
		return
	}
}

func (h Handler) uploadPack(w http.ResponseWriter, r *http.Request, repo repopath.Repository) {
	t, ep, err := h.transport(repo)
	if err != nil {
		http.Error(w, "not found", 404)
		return
	}
	s, err := t.NewUploadPackSession(ep, nil)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	defer s.Close()
	req := packp.NewUploadPackRequest()
	if err = req.Decode(r.Body); err != nil {
		http.Error(w, "bad upload-pack request", 400)
		return
	}
	resp, err := s.UploadPack(r.Context(), req)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer resp.Close()
	w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
	w.Header().Set("Cache-Control", "no-cache")
	_ = resp.Encode(w)
}

func (h Handler) receivePack(w http.ResponseWriter, r *http.Request, repo repopath.Repository) {
	t, ep, err := h.transport(repo)
	if err != nil {
		http.Error(w, "not found", 404)
		return
	}
	s, err := t.NewReceivePackSession(ep, nil)
	if err != nil {
		http.Error(w, err.Error(), 404)
		return
	}
	defer s.Close()
	req := packp.NewReferenceUpdateRequest()
	if err = req.Decode(r.Body); err != nil {
		http.Error(w, "bad receive-pack request", 400)
		return
	}
	if repo.Name == "control" {
		if err := validateControlRefs(req); err != nil {
			http.Error(w, err.Error(), 403)
			return
		}
	}
	status, err := s.ReceivePack(r.Context(), req)
	w.Header().Set("Content-Type", "application/x-git-receive-pack-result")
	w.Header().Set("Cache-Control", "no-cache")
	if status != nil {
		_ = status.Encode(w)
	}
	if err != nil {
		return
	}
}

func validateControlRefs(req *packp.ReferenceUpdateRequest) error {
	if len(req.Commands) != 1 {
		return fmt.Errorf("control repository permits one main update only")
	}
	c := req.Commands[0]
	if c.Name.String() != "refs/heads/main" {
		return fmt.Errorf("control repository only permits refs/heads/main")
	}
	if c.New == plumbing.ZeroHash {
		return fmt.Errorf("main cannot be deleted")
	}
	if c.Old == plumbing.ZeroHash {
		return fmt.Errorf("main can only be created during bootstrap")
	}
	return nil
}
