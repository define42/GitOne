package githttp

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/define42/GitOne/internal/control"
	"github.com/define42/GitOne/internal/lockmgr"
	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/storage"
	"github.com/go-git/go-billy/v5/osfs"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/format/pktline"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp/capability"
	"github.com/go-git/go-git/v5/plumbing/transport"
	gitserver "github.com/go-git/go-git/v5/plumbing/transport/server"
)

const noThinCapability capability.Capability = "no-thin"

type Authorizer func(*http.Request, repopath.Repository, bool) (authenticated, allowed bool)

type ReferenceUpdate struct {
	Branch string
	Commit plumbing.Hash
}

type Handler struct {
	Storage           storage.Store
	Authorize         Authorizer
	ControlUpdated    func(string)
	RepositoryUpdated func(repopath.Repository, []ReferenceUpdate)
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	repo, suffix, err := repopath.ParseGitRequestPath(r.URL.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	write := suffix == "/git-receive-pack" || (suffix == "/info/refs" && r.URL.Query().Get("service") == "git-receive-pack")
	if !h.authorize(w, r, repo, write) {
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

func (h Handler) authorize(
	w http.ResponseWriter,
	r *http.Request,
	repository repopath.Repository,
	write bool,
) bool {
	if h.Authorize == nil {
		return true
	}
	authenticated, allowed := h.Authorize(r, repository, write)
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
		defer func() {
			_ = s.Close()
		}()
		adv, err = s.AdvertisedReferencesContext(r.Context())
	} else {
		s, e := t.NewReceivePackSession(ep, nil)
		if e != nil {
			http.Error(w, e.Error(), 404)
			return
		}
		defer func() {
			_ = s.Close()
		}()
		adv, err = s.AdvertisedReferencesContext(r.Context())
		if err == nil {
			// The filesystem pack writer cannot resolve bases omitted by thin packs.
			err = adv.Capabilities.Set(noThinCapability)
		}
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
	defer func() {
		_ = s.Close()
	}()
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
	defer func() {
		_ = resp.Close()
	}()
	w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
	w.Header().Set("Cache-Control", "no-cache")
	_ = resp.Encode(w)
}

func (h Handler) receivePack(w http.ResponseWriter, r *http.Request, repo repopath.Repository) {
	req := packp.NewReferenceUpdateRequest()
	if err := req.Decode(r.Body); err != nil {
		http.Error(w, "bad receive-pack request", 400)
		return
	}
	defer func() {
		_ = req.Packfile.Close()
	}()
	if err := validateReceiveCapabilities(req.Capabilities); err != nil {
		h.writeReceiveError(w, req, "ok", err)
		return
	}

	if repo.Name == "control" {
		if err := validateControlRefs(req); err != nil {
			h.writeReceiveError(w, req, "ok", err)
			return
		}
	}

	releaseOperation, err := lockmgr.Process.Acquire(
		lockmgr.RepositoryRequests(
			h.Storage.Root,
			[]repopath.Repository{repo},
			lockmgr.Exclusive,
		)...,
	)
	if err != nil {
		h.writeReceiveError(w, req, err.Error(), err)
		return
	}
	defer func() {
		releaseOperation()
	}()
	if !h.authorize(w, r, repo, true) {
		return
	}
	path, err := h.Storage.GitPath(repo)
	if err != nil {
		http.Error(w, "not found", 404)
		return
	}
	repository, err := git.PlainOpen(path)
	if err != nil {
		http.Error(w, "not found", 404)
		return
	}

	if err = validateReferenceCommands(repository, req); err != nil {
		h.writeReceiveError(w, req, "ok", err)
		return
	}
	reader := bufio.NewReader(req.Packfile)
	if _, peekErr := reader.Peek(1); peekErr == nil {
		if err = packfile.UpdateObjectStorage(repository.Storer, reader); err != nil {
			h.writeReceiveError(w, req, err.Error(), err)
			return
		}
	} else if !errors.Is(peekErr, io.EOF) {
		h.writeReceiveError(w, req, peekErr.Error(), peekErr)
		return
	}

	if repo.Name == "control" {
		if err = validateControlUpdate(repository, repo.Group(), req.Commands[0]); err != nil {
			h.writeReceiveError(w, req, "ok", err)
			return
		}
	} else if err = validateLFSPointerUpdates(
		repository,
		h.Storage,
		repo,
		req.Commands,
	); err != nil {
		h.writeReceiveError(w, req, "ok", err)
		return
	}

	status := packp.NewReportStatus()
	status.UnpackStatus = "ok"
	allApplied := true
	updates := make([]ReferenceUpdate, 0, len(req.Commands))
	for _, command := range req.Commands {
		err = applyReferenceCommand(repository, command)
		commandStatus := &packp.CommandStatus{
			ReferenceName: command.Name,
			Status:        "ok",
		}
		if err != nil {
			commandStatus.Status = err.Error()
			allApplied = false
		} else if repo.Name != "control" &&
			command.New != plumbing.ZeroHash &&
			command.Name.IsBranch() {
			updates = append(updates, ReferenceUpdate{
				Branch: command.Name.Short(),
				Commit: command.New,
			})
		}
		status.CommandStatuses = append(status.CommandStatuses, commandStatus)
	}
	h.writeReceiveStatus(w, req, status)
	if allApplied && repo.Name == "control" && h.ControlUpdated != nil {
		h.ControlUpdated(repo.Group())
	}
	if len(updates) > 0 && h.RepositoryUpdated != nil {
		h.RepositoryUpdated(repo, updates)
	}
}

func validateReceiveCapabilities(capabilities *capability.List) error {
	for _, requested := range capabilities.All() {
		switch requested {
		case capability.Agent,
			capability.OFSDelta,
			capability.DeleteRefs,
			capability.ReportStatus,
			noThinCapability:
		default:
			return fmt.Errorf("unsupported receive capability %q", requested)
		}
	}
	return nil
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
		return fmt.Errorf("main can only be created during repository initialization")
	}
	return nil
}

func validateReferenceCommands(repository *git.Repository, req *packp.ReferenceUpdateRequest) error {
	for _, command := range req.Commands {
		current, err := repository.Reference(command.Name, false)
		switch command.Action() {
		case packp.Create:
			if err == nil {
				return fmt.Errorf("%s already exists", command.Name)
			}
			if !errors.Is(err, plumbing.ErrReferenceNotFound) {
				return fmt.Errorf("read %s: %w", command.Name, err)
			}
		case packp.Update, packp.Delete:
			if err != nil {
				return fmt.Errorf("%s does not exist", command.Name)
			}
			if current.Hash() != command.Old {
				return fmt.Errorf(
					"stale reference %s: expected %s, found %s",
					command.Name,
					command.Old,
					current.Hash(),
				)
			}
		default:
			return fmt.Errorf("invalid update for %s", command.Name)
		}
	}
	return nil
}

func validateControlUpdate(repository *git.Repository, group string, command *packp.Command) error {
	oldCommit, err := repository.CommitObject(command.Old)
	if err != nil {
		return fmt.Errorf("load current control commit: %w", err)
	}
	newCommit, err := repository.CommitObject(command.New)
	if err != nil {
		return fmt.Errorf("new control revision must be a commit: %w", err)
	}
	fastForward, err := oldCommit.IsAncestor(newCommit)
	if err != nil {
		return fmt.Errorf("check control history: %w", err)
	}
	if !fastForward {
		return errors.New("control updates must be fast-forward")
	}
	current, err := control.ReadDocument(repository, command.Old, group)
	if err != nil {
		return fmt.Errorf("invalid control.json at current revision: %w", err)
	}
	updated, err := control.ReadDocument(repository, command.New, group)
	if err != nil {
		return fmt.Errorf("invalid control.json: %w", err)
	}
	currentTokenHashes := make(map[string]string, len(current.Tokens))
	for _, token := range current.Tokens {
		currentTokenHashes[token.Key] = token.Hash
	}
	for _, token := range updated.Tokens {
		hash, exists := currentTokenHashes[token.Key]
		if !exists || hash != token.Hash {
			return errors.New(
				"token secrets can only be generated or regenerated through the group settings API",
			)
		}
	}
	return nil
}

func applyReferenceCommand(repository *git.Repository, command *packp.Command) error {
	switch command.Action() {
	case packp.Create:
		if _, err := repository.Reference(command.Name, false); err == nil {
			return fmt.Errorf("%s already exists", command.Name)
		} else if !errors.Is(err, plumbing.ErrReferenceNotFound) {
			return err
		}
		return repository.Storer.SetReference(plumbing.NewHashReference(command.Name, command.New))
	case packp.Update:
		return repository.Storer.CheckAndSetReference(
			plumbing.NewHashReference(command.Name, command.New),
			plumbing.NewHashReference(command.Name, command.Old),
		)
	case packp.Delete:
		current, err := repository.Reference(command.Name, false)
		if err != nil {
			return err
		}
		if current.Hash() != command.Old {
			return fmt.Errorf("stale reference %s", command.Name)
		}
		return repository.Storer.RemoveReference(command.Name)
	default:
		return fmt.Errorf("invalid update for %s", command.Name)
	}
}

func (h Handler) writeReceiveError(
	w http.ResponseWriter,
	req *packp.ReferenceUpdateRequest,
	unpack string,
	err error,
) {
	if !req.Capabilities.Supports(capability.ReportStatus) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	status := packp.NewReportStatus()
	status.UnpackStatus = unpack
	for _, command := range req.Commands {
		status.CommandStatuses = append(status.CommandStatuses, &packp.CommandStatus{
			ReferenceName: command.Name,
			Status:        err.Error(),
		})
	}
	h.writeReceiveStatus(w, req, status)
}

func (h Handler) writeReceiveStatus(
	w http.ResponseWriter,
	req *packp.ReferenceUpdateRequest,
	status *packp.ReportStatus,
) {
	w.Header().Set("Content-Type", "application/x-git-receive-pack-result")
	w.Header().Set("Cache-Control", "no-cache")
	if req.Capabilities.Supports(capability.ReportStatus) {
		_ = status.Encode(w)
	}
}
