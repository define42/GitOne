package githttp

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/define42/GitOne/internal/auth"
	"github.com/define42/GitOne/internal/control"
	"github.com/define42/GitOne/internal/gitformat"
	"github.com/define42/GitOne/internal/httpio"
	"github.com/define42/GitOne/internal/lockmgr"
	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/storage"
	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/format/packfile"
	"github.com/go-git/go-git/v6/plumbing/format/pktline"
	"github.com/go-git/go-git/v6/plumbing/protocol/capability"
	"github.com/go-git/go-git/v6/plumbing/protocol/packp"
	"github.com/go-git/go-git/v6/plumbing/protocol/packp/sideband"
	"github.com/go-git/go-git/v6/plumbing/transport"
)

const (
	noThinCapability                capability.Capability = "no-thin"
	maximumUploadPackRequestBytes   int64                 = 16 << 20
	maximumReceivePackRequestBytes  int64                 = 1 << 30
	maximumRepositoryGitObjectBytes int64                 = 20 << 30
)

type Authorizer func(*http.Request, repopath.Repository, bool) (authenticated, allowed bool)

type ReferenceUpdate struct {
	Branch string
	Commit plumbing.Hash
}

type responseWriteCloser struct {
	io.Writer
}

func (responseWriteCloser) Close() error { return nil }

type Handler struct {
	Storage           storage.Store
	Authorize         Authorizer
	ControlUpdated    func(string)
	RepositoryUpdated func(repopath.Repository, []ReferenceUpdate)
	// MaximumRepositoryGitObjectBytes overrides the default Git object quota.
	// Non-positive values use the default.
	MaximumRepositoryGitObjectBytes int64
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	repo, suffix, err := repopath.ParseGitRequestPath(r.URL.Path)
	maximumBodyBytes := int64(0)
	if err == nil {
		switch suffix {
		case "/git-upload-pack":
			maximumBodyBytes = maximumUploadPackRequestBytes
		case "/git-receive-pack":
			maximumBodyBytes = maximumReceivePackRequestBytes
		}
	}
	w, cleanup := httpio.Protect(
		w,
		r,
		httpio.DefaultIdleTimeout,
		maximumBodyBytes,
	)
	defer cleanup()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if r.ContentLength > maximumBodyBytes {
		http.Error(w, "Git request body is too large", http.StatusRequestEntityTooLarge)
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
	if retryAfter, limited := auth.RequestRateLimit(r.Context()); limited {
		seconds := max(1, int((retryAfter+time.Second-1)/time.Second))
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		http.Error(w, "too many authentication attempts", http.StatusTooManyRequests)
		return false
	}
	if !authenticated {
		w.Header().Set("WWW-Authenticate", `Basic realm="GitOne"`)
		http.Error(w, "authentication required", http.StatusUnauthorized)
	} else {
		http.Error(w, "forbidden", http.StatusForbidden)
	}
	return false
}

func (h Handler) openRepository(repo repopath.Repository) (*git.Repository, error) {
	p, err := h.Storage.GitPath(repo)
	if err != nil {
		return nil, err
	}
	return gitformat.Open(p)
}

func (h Handler) advertise(w http.ResponseWriter, r *http.Request, repo repopath.Repository) {
	service := r.URL.Query().Get("service")
	if service != "git-upload-pack" && service != "git-receive-pack" {
		http.Error(w, "unsupported service", 400)
		return
	}
	repository, err := h.openRepository(repo)
	if err != nil {
		http.Error(w, "repository not found", 404)
		return
	}
	w.Header().Set("Content-Type", "application/x-"+service+"-advertisement")
	w.Header().Set("Cache-Control", "no-cache")
	writer := responseWriteCloser{Writer: w}
	if service == "git-upload-pack" {
		err = transport.UploadPack(
			r.Context(),
			repository.Storer,
			nil,
			writer,
			&transport.UploadPackRequest{
				GitProtocol:   r.Header.Get("Git-Protocol"),
				AdvertiseRefs: true,
				StatelessRPC:  true,
			},
		)
	} else {
		err = advertiseSHA256ReceivePack(
			repository.Storer,
			r.Header.Get("Git-Protocol"),
			writer,
		)
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
}

func (h Handler) uploadPack(w http.ResponseWriter, r *http.Request, repo repopath.Repository) {
	repository, err := h.openRepository(repo)
	if err != nil {
		http.Error(w, "not found", 404)
		return
	}
	w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
	w.Header().Set("Cache-Control", "no-cache")
	err = transport.UploadPack(
		r.Context(),
		repository.Storer,
		r.Body,
		responseWriteCloser{Writer: w},
		&transport.UploadPackRequest{
			GitProtocol:  r.Header.Get("Git-Protocol"),
			StatelessRPC: true,
		},
	)
	if err != nil {
		if httpio.BodyTooLarge(err) {
			http.Error(w, "upload-pack request is too large", http.StatusRequestEntityTooLarge)
			return
		}
		if strings.HasPrefix(err.Error(), "peeking line:") ||
			strings.HasPrefix(err.Error(), "decoding upload-") {
			http.Error(w, "bad upload-pack request", http.StatusBadRequest)
			return
		}
		http.Error(w, err.Error(), 500)
		return
	}
}

func (h Handler) receivePack(w http.ResponseWriter, r *http.Request, repo repopath.Repository) {
	reader := bufio.NewReader(r.Body)
	req := &packp.UpdateRequests{}
	if err := req.Decode(reader); err != nil {
		if httpio.BodyTooLarge(err) {
			http.Error(w, "receive-pack request is too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "bad receive-pack request", 400)
		return
	}
	if req.Capabilities.Supports(capability.PushOptions) {
		var options packp.PushOptions
		if err := options.Decode(reader); err != nil {
			http.Error(w, "bad receive-pack push options", http.StatusBadRequest)
			return
		}
	}
	if err := validateReceiveCapabilities(&req.Capabilities); err != nil {
		h.writeReceiveError(w, req, "ok", err)
		return
	}
	if err := validateReceiveObjectIDs(req); err != nil {
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
	repository, err := gitformat.Open(path)
	if err != nil {
		http.Error(w, "not found", 404)
		return
	}

	if err = validateReferenceCommands(repository, req); err != nil {
		h.writeReceiveError(w, req, "ok", err)
		return
	}
	validationRepository := repository
	var quarantine *receiveQuarantine
	if _, peekErr := reader.Peek(1); peekErr == nil {
		quarantine, err = newReceiveQuarantine(path, repository.Storer)
		if err != nil {
			h.writeReceiveError(w, req, err.Error(), err)
			return
		}
		defer quarantine.Remove()
		validationRepository = quarantine.Repository
		if err = packfile.UpdateObjectStorage(validationRepository.Storer, reader); err != nil {
			if httpio.BodyTooLarge(err) {
				http.Error(w, "receive-pack request is too large", http.StatusRequestEntityTooLarge)
				return
			}
			h.writeReceiveError(w, req, err.Error(), err)
			return
		}
	} else if !errors.Is(peekErr, io.EOF) {
		if httpio.BodyTooLarge(peekErr) {
			http.Error(w, "receive-pack request is too large", http.StatusRequestEntityTooLarge)
			return
		}
		h.writeReceiveError(w, req, peekErr.Error(), peekErr)
		return
	}

	if repo.Name == "control" {
		if err = validateControlUpdate(validationRepository, repo.Group(), req.Commands[0]); err != nil {
			h.writeReceiveError(w, req, "ok", err)
			return
		}
	} else if err = validateLFSPointerUpdates(
		validationRepository,
		h.Storage,
		repo,
		req.Commands,
	); err != nil {
		h.writeReceiveError(w, req, "ok", err)
		return
	}

	maintenanceCandidates := make(map[*packp.Command]bool, len(req.Commands))
	for _, command := range req.Commands {
		maintenanceCandidates[command] = referenceUpdateMayDiscardObjects(
			validationRepository,
			command,
		)
	}

	if quarantine != nil {
		maximumBytes := h.MaximumRepositoryGitObjectBytes
		if maximumBytes <= 0 {
			maximumBytes = maximumRepositoryGitObjectBytes
		}
		if err = enforceRepositoryObjectQuota(path, quarantine.Root, maximumBytes); err != nil {
			h.writeReceiveError(w, req, "ok", err)
			return
		}
		if err = quarantine.Promote(path); err != nil {
			h.writeReceiveError(w, req, err.Error(), err)
			return
		}
	}

	status := &packp.ReportStatus{UnpackStatus: "ok"}
	allApplied := true
	maintenanceNeeded := false
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
		} else {
			maintenanceNeeded = maintenanceNeeded || maintenanceCandidates[command]
			if repo.Name != "control" &&
				!command.New.IsZero() &&
				command.Name.IsBranch() {
				updates = append(updates, ReferenceUpdate{
					Branch: command.Name.Short(),
					Commit: command.New,
				})
			}
		}
		status.CommandStatuses = append(status.CommandStatuses, commandStatus)
	}
	if maintenanceNeeded {
		if err = maintainRepositoryObjects(path); err != nil {
			log.Printf("could not maintain Git objects for %s: %v", repo.Full(), err)
		}
	}
	h.writeReceiveStatus(w, req, status)
	if allApplied && repo.Name == "control" && h.ControlUpdated != nil {
		h.ControlUpdated(repo.Group())
	}
	if len(updates) > 0 && h.RepositoryUpdated != nil {
		h.RepositoryUpdated(repo, updates)
	}
}

func referenceUpdateMayDiscardObjects(
	repository *git.Repository,
	command *packp.Command,
) bool {
	switch command.Action() {
	case packp.Delete:
		return true
	case packp.Update:
		if !command.Name.IsBranch() {
			return true
		}
		oldCommit, err := repository.CommitObject(command.Old)
		if err != nil {
			return true
		}
		newCommit, err := repository.CommitObject(command.New)
		if err != nil {
			return true
		}
		fastForward, err := oldCommit.IsAncestor(newCommit)
		return err != nil || !fastForward
	default:
		return false
	}
}

func validateReceiveCapabilities(capabilities *capability.List) error {
	objectFormats := capabilities.Get(capability.ObjectFormat)
	// Native Git echoes the advertised object-format. go-git/v6 alpha.5 does
	// not yet echo it on push, so an omitted value is accepted only because
	// validateReceiveObjectIDs independently requires 32-byte object IDs and
	// the SHA-256 quarantine rejects a pack in any other format.
	if len(objectFormats) > 0 &&
		(len(objectFormats) != 1 || objectFormats[0] != gitSHA256ObjectFormat) {
		return fmt.Errorf("receive object-format must be sha256")
	}
	for _, requested := range capabilities.All() {
		switch requested {
		case capability.ObjectFormat:
		case capability.Agent,
			capability.OFSDelta,
			capability.DeleteRefs,
			capability.ReportStatus,
			capability.ReportStatusV2,
			capability.Sideband,
			capability.Sideband64k,
			capability.NoProgress,
			capability.PushOptions,
			capability.Quiet,
			noThinCapability:
		default:
			return fmt.Errorf("unsupported receive capability %q", requested)
		}
	}
	return nil
}

func validateControlRefs(req *packp.UpdateRequests) error {
	if len(req.Commands) != 1 {
		return fmt.Errorf("control repository permits one main update only")
	}
	c := req.Commands[0]
	if c.Name.String() != "refs/heads/main" {
		return fmt.Errorf("control repository only permits refs/heads/main")
	}
	if c.New.IsZero() {
		return fmt.Errorf("main cannot be deleted")
	}
	if c.Old.IsZero() {
		return fmt.Errorf("main can only be created during repository initialization")
	}
	return nil
}

func validateReferenceCommands(repository *git.Repository, req *packp.UpdateRequests) error {
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
	req *packp.UpdateRequests,
	unpack string,
	err error,
) {
	if !req.Capabilities.Supports(capability.ReportStatus) &&
		!req.Capabilities.Supports(capability.ReportStatusV2) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	status := &packp.ReportStatus{UnpackStatus: unpack}
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
	req *packp.UpdateRequests,
	status *packp.ReportStatus,
) {
	w.Header().Set("Content-Type", "application/x-git-receive-pack-result")
	w.Header().Set("Cache-Control", "no-cache")
	if !req.Capabilities.Supports(capability.ReportStatus) &&
		!req.Capabilities.Supports(capability.ReportStatusV2) {
		return
	}
	var writer io.Writer = w
	sidebanded := false
	if req.Capabilities.Supports(capability.Sideband64k) {
		writer = sideband.NewMuxer(sideband.Sideband64k, w)
		sidebanded = true
	} else if req.Capabilities.Supports(capability.Sideband) {
		writer = sideband.NewMuxer(sideband.Sideband, w)
		sidebanded = true
	}
	if err := status.Encode(writer); err == nil && sidebanded {
		_ = pktline.WriteFlush(w)
	}
}
