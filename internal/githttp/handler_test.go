package githttp

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/define42/GitOne/internal/control"
	"github.com/define42/GitOne/internal/lfs"
	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/review"
	"github.com/define42/GitOne/internal/storage"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp/capability"
)

func TestControlRefValidation(t *testing.T) {
	req := packp.NewReferenceUpdateRequest()
	req.Commands = []*packp.Command{{Name: plumbing.NewBranchReferenceName("main"), Old: plumbing.NewHash("1111111111111111111111111111111111111111"), New: plumbing.NewHash("2222222222222222222222222222222222222222")}}
	if e := validateControlRefs(req); e != nil {
		t.Fatal(e)
	}
}

func TestGitRequestBodyLimitsRejectDeclaredOversize(t *testing.T) {
	for _, test := range []struct {
		name    string
		path    string
		maximum int64
	}{
		{
			name:    "upload pack",
			path:    "/engineering/api.git/git-upload-pack",
			maximum: maximumUploadPackRequestBytes,
		},
		{
			name:    "receive pack",
			path:    "/engineering/api.git/git-receive-pack",
			maximum: maximumReceivePackRequestBytes,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, test.path, http.NoBody)
			request.ContentLength = test.maximum + 1
			response := httptest.NewRecorder()

			(Handler{}).ServeHTTP(response, request)

			if response.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestControlRejectsTags(t *testing.T) {
	req := packp.NewReferenceUpdateRequest()
	req.Commands = []*packp.Command{{Name: plumbing.NewTagReferenceName("v1"), Old: plumbing.NewHash("1111111111111111111111111111111111111111"), New: plumbing.NewHash("2222222222222222222222222222222222222222")}}
	if e := validateControlRefs(req); e == nil {
		t.Fatal("expected rejection")
	}
}

func TestControlRefValidationRejectsUnsafeMainChanges(t *testing.T) {
	hash := plumbing.NewHash("1111111111111111111111111111111111111111")
	for _, test := range []struct {
		name     string
		commands []*packp.Command
	}{
		{name: "no commands"},
		{
			name: "multiple commands",
			commands: []*packp.Command{
				{Name: plumbing.NewBranchReferenceName("main"), Old: hash, New: hash},
				{Name: plumbing.NewBranchReferenceName("other"), Old: hash, New: hash},
			},
		},
		{
			name: "delete main",
			commands: []*packp.Command{{
				Name: plumbing.NewBranchReferenceName("main"),
				Old:  hash,
				New:  plumbing.ZeroHash,
			}},
		},
		{
			name: "create main",
			commands: []*packp.Command{{
				Name: plumbing.NewBranchReferenceName("main"),
				Old:  plumbing.ZeroHash,
				New:  hash,
			}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := packp.NewReferenceUpdateRequest()
			request.Commands = test.commands
			if err := validateControlRefs(request); err == nil {
				t.Fatal("unsafe control reference update was accepted")
			}
		})
	}
}

func TestValidateControlUpdateWithStoredHistory(t *testing.T) {
	store := storage.Store{Root: t.TempDir()}
	if err := store.CreateGroup("engineering", "alice", "initial"); err != nil {
		t.Fatal(err)
	}
	repositoryPath := repopath.Repository{Groups: []string{"engineering"}, Name: "control"}
	path, err := store.GitPath(repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := git.PlainOpen(path)
	if err != nil {
		t.Fatal(err)
	}
	head, err := repository.Reference(plumbing.NewBranchReferenceName("main"), true)
	if err != nil {
		t.Fatal(err)
	}
	initial := head.Hash()
	controls := control.NewStore(store.Root)
	document, err := controls.Load(t.Context(), "engineering")
	if err != nil {
		t.Fatal(err)
	}
	document.Description = "updated"
	if err = store.UpdateGroupControl("engineering", document, "alice"); err != nil {
		t.Fatal(err)
	}
	head, err = repository.Reference(plumbing.NewBranchReferenceName("main"), true)
	if err != nil {
		t.Fatal(err)
	}
	updated := head.Hash()
	command := &packp.Command{
		Name: plumbing.NewBranchReferenceName("main"),
		Old:  initial,
		New:  updated,
	}
	if err = validateControlUpdate(repository, "engineering", command); err != nil {
		t.Fatalf("valid fast-forward control update: %v", err)
	}

	document.Tokens = []control.Token{{
		Name: "automation",
		Key:  "ci",
		Hash: "user-managed-hash",
		Role: control.RoleDeveloper,
	}}
	if err = store.UpdateGroupControl("engineering", document, "alice"); err != nil {
		t.Fatal(err)
	}
	head, err = repository.Reference(plumbing.NewBranchReferenceName("main"), true)
	if err != nil {
		t.Fatal(err)
	}
	tokenAdded := head.Hash()
	command.Old = updated
	command.New = tokenAdded
	if err = validateControlUpdate(repository, "engineering", command); err == nil ||
		!strings.Contains(err.Error(), "group settings API") {
		t.Fatalf("user-managed token secret returned %v", err)
	}

	document.Tokens[0].Name = "renamed automation"
	if err = store.UpdateGroupControl("engineering", document, "alice"); err != nil {
		t.Fatal(err)
	}
	head, err = repository.Reference(plumbing.NewBranchReferenceName("main"), true)
	if err != nil {
		t.Fatal(err)
	}
	tokenRenamed := head.Hash()
	command.Old = tokenAdded
	command.New = tokenRenamed
	if err = validateControlUpdate(repository, "engineering", command); err != nil {
		t.Fatalf("token metadata update with preserved secret: %v", err)
	}

	document.Tokens[0].Hash = "replacement-user-managed-hash"
	if err = store.UpdateGroupControl("engineering", document, "alice"); err != nil {
		t.Fatal(err)
	}
	head, err = repository.Reference(plumbing.NewBranchReferenceName("main"), true)
	if err != nil {
		t.Fatal(err)
	}
	tokenRotated := head.Hash()
	command.Old = tokenRenamed
	command.New = tokenRotated
	if err = validateControlUpdate(repository, "engineering", command); err == nil ||
		!strings.Contains(err.Error(), "group settings API") {
		t.Fatalf("user-rotated token secret returned %v", err)
	}

	missing := plumbing.NewHash("4444444444444444444444444444444444444444")
	command.Old = missing
	if err = validateControlUpdate(repository, "engineering", command); err == nil ||
		!strings.Contains(err.Error(), "load current control commit") {
		t.Fatalf("missing old commit returned %v", err)
	}
	command.Old = initial
	command.New = missing
	if err = validateControlUpdate(repository, "engineering", command); err == nil ||
		!strings.Contains(err.Error(), "new control revision") {
		t.Fatalf("missing new commit returned %v", err)
	}
	command.Old = updated
	command.New = initial
	if err = validateControlUpdate(repository, "engineering", command); err == nil ||
		!strings.Contains(err.Error(), "fast-forward") {
		t.Fatalf("non-fast-forward update returned %v", err)
	}
	command.Old = initial
	command.New = updated
	if err = validateControlUpdate(repository, "other-group", command); err == nil ||
		!strings.Contains(err.Error(), "invalid control.json") {
		t.Fatalf("mismatched control document returned %v", err)
	}
}

func TestRejectsUnsupportedReceiveCapability(t *testing.T) {
	capabilities := capability.NewList()
	if err := capabilities.Set(capability.Atomic); err != nil {
		t.Fatal(err)
	}
	if err := validateReceiveCapabilities(capabilities); err == nil {
		t.Fatal("expected unsupported atomic capability to be rejected")
	}
}

func TestAuthenticationFailureChallengesGitClient(t *testing.T) {
	handler := Handler{
		Authorize: func(*http.Request, repopath.Repository, bool) (bool, bool) {
			return false, false
		},
	}
	request := httptest.NewRequest(http.MethodGet, "/group/repository.git/info/refs?service=git-upload-pack", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, response.Code)
	}
	if response.Header().Get("WWW-Authenticate") != `Basic realm="GitOne"` {
		t.Fatalf("unexpected authentication challenge: %q", response.Header().Get("WWW-Authenticate"))
	}
}

func TestAuthenticatedUserWithoutPermissionIsForbidden(t *testing.T) {
	handler := Handler{
		Authorize: func(*http.Request, repopath.Repository, bool) (bool, bool) {
			return true, false
		},
	}
	request := httptest.NewRequest(http.MethodGet, "/group/repository.git/info/refs?service=git-upload-pack", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, response.Code)
	}
}

func TestSmartHTTPRoutesAndMalformedRequests(t *testing.T) {
	store := storage.Store{Root: t.TempDir()}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	repositoryPath := repopath.Repository{Groups: []string{"engineering"}, Name: "docs"}
	if err := store.CreateRepository(repositoryPath, storage.CreateRepositoryOptions{
		InitializeReadme: true,
		Author:           "alice",
	}); err != nil {
		t.Fatal(err)
	}
	handler := Handler{Storage: store}

	for _, test := range []struct {
		name, method, path, body string
		status                   int
		contentType              string
	}{
		{
			name:   "invalid repository path",
			method: http.MethodGet,
			path:   "/docs.git/info/refs?service=git-upload-pack",
			status: http.StatusBadRequest,
		},
		{
			name:   "unknown route",
			method: http.MethodGet,
			path:   "/engineering/docs.git",
			status: http.StatusNotFound,
		},
		{
			name:   "unsupported advertised service",
			method: http.MethodGet,
			path:   "/engineering/docs.git/info/refs?service=git-archive",
			status: http.StatusBadRequest,
		},
		{
			name:   "missing advertised repository",
			method: http.MethodGet,
			path:   "/engineering/missing.git/info/refs?service=git-upload-pack",
			status: http.StatusNotFound,
		},
		{
			name:        "advertise upload pack",
			method:      http.MethodGet,
			path:        "/engineering/docs.git/info/refs?service=git-upload-pack",
			status:      http.StatusOK,
			contentType: "application/x-git-upload-pack-advertisement",
		},
		{
			name:        "advertise receive pack",
			method:      http.MethodGet,
			path:        "/engineering/docs.git/info/refs?service=git-receive-pack",
			status:      http.StatusOK,
			contentType: "application/x-git-receive-pack-advertisement",
		},
		{
			name:   "upload pack wrong method",
			method: http.MethodGet,
			path:   "/engineering/docs.git/git-upload-pack",
			status: http.StatusNotFound,
		},
		{
			name:   "receive pack wrong method",
			method: http.MethodGet,
			path:   "/engineering/docs.git/git-receive-pack",
			status: http.StatusNotFound,
		},
		{
			name:   "malformed upload pack",
			method: http.MethodPost,
			path:   "/engineering/docs.git/git-upload-pack",
			body:   "not a packet line",
			status: http.StatusBadRequest,
		},
		{
			name:   "malformed receive pack",
			method: http.MethodPost,
			path:   "/engineering/docs.git/git-receive-pack",
			body:   "not a packet line",
			status: http.StatusBadRequest,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.status, response.Body.String())
			}
			if test.contentType != "" && response.Header().Get("Content-Type") != test.contentType {
				t.Fatalf("content type = %q, want %q", response.Header().Get("Content-Type"), test.contentType)
			}
		})
	}
}

func TestReceiveStatusErrorFormats(t *testing.T) {
	handler := Handler{}
	request := packp.NewReferenceUpdateRequest()
	response := httptest.NewRecorder()
	handler.writeReceiveError(response, request, "ok", errors.New("rejected"))
	if response.Code != http.StatusConflict ||
		!strings.Contains(response.Body.String(), "rejected") {
		t.Fatalf("plain receive error = %d %q", response.Code, response.Body.String())
	}

	if err := request.Capabilities.Set(capability.ReportStatus); err != nil {
		t.Fatal(err)
	}
	request.Commands = []*packp.Command{{
		Name: plumbing.NewBranchReferenceName("main"),
		Old:  plumbing.NewHash("1111111111111111111111111111111111111111"),
		New:  plumbing.NewHash("2222222222222222222222222222222222222222"),
	}}
	response = httptest.NewRecorder()
	handler.writeReceiveError(response, request, "unpack failed", errors.New("rejected"))
	if response.Code != http.StatusOK ||
		response.Header().Get("Content-Type") != "application/x-git-receive-pack-result" ||
		!strings.Contains(response.Body.String(), "unpack failed") ||
		!strings.Contains(response.Body.String(), "rejected") {
		t.Fatalf("report-status receive error = %d %q", response.Code, response.Body.String())
	}
}

func TestRejectsStaleReferenceUpdate(t *testing.T) {
	store := storage.Store{Root: t.TempDir()}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	repositoryPath := repopath.Repository{Groups: []string{"engineering"}, Name: "docs"}
	if err := store.CreateRepository(repositoryPath, storage.CreateRepositoryOptions{
		InitializeReadme: true,
		Author:           "alice",
	}); err != nil {
		t.Fatal(err)
	}
	path, err := store.GitPath(repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := git.PlainOpen(path)
	if err != nil {
		t.Fatal(err)
	}
	head, err := repository.Reference(plumbing.NewBranchReferenceName("main"), true)
	if err != nil {
		t.Fatal(err)
	}
	request := packp.NewReferenceUpdateRequest()
	request.Commands = []*packp.Command{{
		Name: plumbing.NewBranchReferenceName("main"),
		Old:  plumbing.NewHash("1111111111111111111111111111111111111111"),
		New:  head.Hash(),
	}}
	if err = validateReferenceCommands(repository, request); err == nil ||
		!strings.Contains(err.Error(), "stale reference") {
		t.Fatalf("expected stale reference error, got %v", err)
	}
}

func TestValidateReferenceCommands(t *testing.T) {
	repository, err := git.PlainInit(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	existing := plumbing.NewBranchReferenceName("main")
	missing := plumbing.NewBranchReferenceName("feature")
	current := plumbing.NewHash("1111111111111111111111111111111111111111")
	next := plumbing.NewHash("2222222222222222222222222222222222222222")
	stale := plumbing.NewHash("3333333333333333333333333333333333333333")
	if err = repository.Storer.SetReference(plumbing.NewHashReference(existing, current)); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name    string
		command *packp.Command
		wantErr string
	}{
		{
			name: "create missing",
			command: &packp.Command{
				Name: missing,
				Old:  plumbing.ZeroHash,
				New:  next,
			},
		},
		{
			name: "create existing",
			command: &packp.Command{
				Name: existing,
				Old:  plumbing.ZeroHash,
				New:  next,
			},
			wantErr: "already exists",
		},
		{
			name: "update current",
			command: &packp.Command{
				Name: existing,
				Old:  current,
				New:  next,
			},
		},
		{
			name: "delete current",
			command: &packp.Command{
				Name: existing,
				Old:  current,
				New:  plumbing.ZeroHash,
			},
		},
		{
			name: "update missing",
			command: &packp.Command{
				Name: missing,
				Old:  current,
				New:  next,
			},
			wantErr: "does not exist",
		},
		{
			name: "stale update",
			command: &packp.Command{
				Name: existing,
				Old:  stale,
				New:  next,
			},
			wantErr: "stale reference",
		},
		{
			name: "invalid",
			command: &packp.Command{
				Name: missing,
				Old:  plumbing.ZeroHash,
				New:  plumbing.ZeroHash,
			},
			wantErr: "invalid update",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := packp.NewReferenceUpdateRequest()
			request.Commands = []*packp.Command{test.command}
			validationErr := validateReferenceCommands(repository, request)
			switch {
			case test.wantErr == "" && validationErr != nil:
				t.Fatalf("unexpected validation error: %v", validationErr)
			case test.wantErr != "" &&
				(validationErr == nil || !strings.Contains(validationErr.Error(), test.wantErr)):
				t.Fatalf("validation error = %v, want containing %q", validationErr, test.wantErr)
			}
		})
	}
}

func TestApplyReferenceCommand(t *testing.T) {
	repository, err := git.PlainInit(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	name := plumbing.NewBranchReferenceName("feature")
	first := plumbing.NewHash("1111111111111111111111111111111111111111")
	second := plumbing.NewHash("2222222222222222222222222222222222222222")
	third := plumbing.NewHash("3333333333333333333333333333333333333333")

	assertReference := func(want plumbing.Hash) {
		t.Helper()
		reference, referenceErr := repository.Reference(name, false)
		if referenceErr != nil {
			t.Fatalf("read %s: %v", name, referenceErr)
		}
		if reference.Hash() != want {
			t.Fatalf("%s points to %s, want %s", name, reference.Hash(), want)
		}
	}

	create := &packp.Command{
		Name: name,
		Old:  plumbing.ZeroHash,
		New:  first,
	}
	if err = applyReferenceCommand(repository, create); err != nil {
		t.Fatalf("create reference: %v", err)
	}
	assertReference(first)

	duplicateCreate := &packp.Command{
		Name: name,
		Old:  plumbing.ZeroHash,
		New:  second,
	}
	if err = applyReferenceCommand(repository, duplicateCreate); err == nil ||
		!strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate create returned %v", err)
	}
	assertReference(first)

	update := &packp.Command{
		Name: name,
		Old:  first,
		New:  second,
	}
	if err = applyReferenceCommand(repository, update); err != nil {
		t.Fatalf("update reference: %v", err)
	}
	assertReference(second)

	staleUpdate := &packp.Command{
		Name: name,
		Old:  first,
		New:  third,
	}
	if err = applyReferenceCommand(repository, staleUpdate); err == nil {
		t.Fatal("stale update succeeded")
	}
	assertReference(second)

	staleDelete := &packp.Command{
		Name: name,
		Old:  first,
		New:  plumbing.ZeroHash,
	}
	if err = applyReferenceCommand(repository, staleDelete); err == nil ||
		!strings.Contains(err.Error(), "stale reference") {
		t.Fatalf("stale delete returned %v", err)
	}
	assertReference(second)

	deleteCommand := &packp.Command{
		Name: name,
		Old:  second,
		New:  plumbing.ZeroHash,
	}
	if err = applyReferenceCommand(repository, deleteCommand); err != nil {
		t.Fatalf("delete reference: %v", err)
	}
	if _, err = repository.Reference(name, false); !errors.Is(err, plumbing.ErrReferenceNotFound) {
		t.Fatalf("deleted reference still exists or returned unexpected error: %v", err)
	}

	if err = applyReferenceCommand(repository, deleteCommand); !errors.Is(err, plumbing.ErrReferenceNotFound) {
		t.Fatalf("delete missing reference returned %v", err)
	}

	invalid := &packp.Command{
		Name: name,
		Old:  plumbing.ZeroHash,
		New:  plumbing.ZeroHash,
	}
	if err = applyReferenceCommand(repository, invalid); err == nil ||
		!strings.Contains(err.Error(), "invalid update") {
		t.Fatalf("invalid command returned %v", err)
	}
}

func TestPartialReceiveNotifiesSuccessfulReferenceUpdates(t *testing.T) {
	store := storage.Store{Root: t.TempDir()}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	repositoryPath := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	if err := store.CreateRepository(repositoryPath, storage.CreateRepositoryOptions{
		InitializeReadme: true,
		Author:           "alice",
	}); err != nil {
		t.Fatal(err)
	}
	path, err := store.GitPath(repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := git.PlainOpen(path)
	if err != nil {
		t.Fatal(err)
	}
	head, err := repository.Reference(plumbing.NewBranchReferenceName("main"), true)
	if err != nil {
		t.Fatal(err)
	}

	feature := plumbing.NewBranchReferenceName("feature")
	request := packp.NewReferenceUpdateRequest()
	if err = request.Capabilities.Set(capability.ReportStatus); err != nil {
		t.Fatal(err)
	}
	request.Commands = []*packp.Command{
		{Name: feature, Old: plumbing.ZeroHash, New: head.Hash()},
		{Name: feature, Old: plumbing.ZeroHash, New: head.Hash()},
	}
	var body bytes.Buffer
	if err = request.Encode(&body); err != nil {
		t.Fatal(err)
	}

	var notifications [][]ReferenceUpdate
	handler := Handler{
		Storage: store,
		RepositoryUpdated: func(repository repopath.Repository, updates []ReferenceUpdate) {
			if repository.Full() != repositoryPath.Full() {
				t.Fatalf("notification repository = %q", repository.Full())
			}
			notifications = append(notifications, updates)
		},
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(
		response,
		httptest.NewRequest(
			http.MethodPost,
			"/engineering/api.git/git-receive-pack",
			&body,
		),
	)

	if response.Code != http.StatusOK {
		t.Fatalf("receive-pack returned %d: %s", response.Code, response.Body.String())
	}
	if len(notifications) != 1 ||
		len(notifications[0]) != 1 ||
		notifications[0][0].Branch != "feature" ||
		notifications[0][0].Commit != head.Hash() {
		t.Fatalf("unexpected repository notifications: %#v", notifications)
	}
	if !strings.Contains(response.Body.String(), "already exists") {
		t.Fatalf("failed sibling update was not reported: %q", response.Body.String())
	}
}

func TestNativeGitRejectsInvalidControlDocument(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable is not available")
	}
	store := storage.Store{Root: t.TempDir()}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(Handler{Storage: store})
	defer server.Close()
	checkout := filepath.Join(t.TempDir(), "control")
	runGit(t, "", "clone", server.URL+"/engineering/control.git", checkout)
	runGit(t, checkout, "config", "user.name", "alice")
	runGit(t, checkout, "config", "user.email", "alice@localhost")

	controlPath := filepath.Join(checkout, "control.json")
	contents, err := os.ReadFile(controlPath)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = json.Unmarshal(contents, &document); err != nil {
		t.Fatal(err)
	}
	document["members"] = map[string]string{"alice": "read"}
	contents, err = json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(controlPath, append(contents, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, checkout, "add", "control.json")
	runGit(t, checkout, "commit", "-m", "Remove final owner")
	rejectedCommit := strings.TrimSpace(runGitOutput(t, checkout, "rev-parse", "HEAD"))
	command := exec.Command("git", "push", "origin", "main")
	command.Dir = checkout
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("invalid control push succeeded:\n%s", output)
	}
	if !strings.Contains(string(output), "at least one owner required") {
		t.Fatalf("push did not report control validation failure:\n%s", output)
	}
	assertServerObjectMissing(
		t,
		store,
		repopath.Repository{Groups: []string{"engineering"}, Name: "control"},
		rejectedCommit,
	)
}

func TestNativeGitRejectsUserManagedTokenSecret(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable is not available")
	}
	store := storage.Store{Root: t.TempDir()}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(Handler{Storage: store})
	defer server.Close()
	checkout := filepath.Join(t.TempDir(), "control")
	runGit(t, "", "clone", server.URL+"/engineering/control.git", checkout)
	runGit(t, checkout, "config", "user.name", "alice")
	runGit(t, checkout, "config", "user.email", "alice@localhost")

	controlPath := filepath.Join(checkout, "control.json")
	contents, err := os.ReadFile(controlPath)
	if err != nil {
		t.Fatal(err)
	}
	var document control.Document
	if err = json.Unmarshal(contents, &document); err != nil {
		t.Fatal(err)
	}
	document.Tokens = []control.Token{{
		Name: "automation",
		Key:  "ci",
		Hash: "user-managed-hash",
		Role: control.RoleDeveloper,
	}}
	contents, err = json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(controlPath, append(contents, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, checkout, "add", "control.json")
	runGit(t, checkout, "commit", "-m", "Add user-managed token secret")
	command := exec.Command("git", "push", "origin", "main")
	command.Dir = checkout
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("user-managed token secret push succeeded:\n%s", output)
	}
	if !strings.Contains(string(output), "group settings API") {
		t.Fatalf("push did not report token secret policy failure:\n%s", output)
	}
}

func TestNativeGitPushValidatesLFSPointers(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable is not available")
	}
	store := storage.Store{Root: t.TempDir()}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	repositoryPath := repopath.Repository{Groups: []string{"engineering"}, Name: "assets"}
	if err := store.CreateRepository(repositoryPath, storage.CreateRepositoryOptions{
		InitializeReadme: true,
		Author:           "alice",
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(Handler{Storage: store})
	defer server.Close()

	checkout := filepath.Join(t.TempDir(), "assets")
	runGit(t, "", "clone", server.URL+"/engineering/assets.git", checkout)
	runGit(t, checkout, "config", "user.name", "alice")
	runGit(t, checkout, "config", "user.email", "alice@localhost")

	content := []byte("large asset stored outside Git")
	sum := sha256.Sum256(content)
	oid := hex.EncodeToString(sum[:])
	pointer := fmt.Sprintf(
		"version https://git-lfs.github.com/spec/v1\noid sha256:%s\nsize %d\n",
		oid,
		len(content),
	)
	assetPath := filepath.Join(checkout, "media", "asset.bin")
	if err := os.MkdirAll(filepath.Dir(assetPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(assetPath, []byte(pointer), 0o640); err != nil {
		t.Fatal(err)
	}
	runGit(t, checkout, "add", "media/asset.bin")
	runGit(t, checkout, "commit", "-m", "Add LFS asset")
	missingObjectCommit := strings.TrimSpace(runGitOutput(t, checkout, "rev-parse", "HEAD"))

	push := exec.Command("git", "push", "origin", "main")
	push.Dir = checkout
	push.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := push.CombinedOutput()
	if err == nil {
		t.Fatalf("push with missing LFS object succeeded:\n%s", output)
	}
	if !strings.Contains(string(output), "invalid LFS pointer") {
		t.Fatalf("push did not report the missing LFS object:\n%s", output)
	}
	assertServerObjectMissing(t, store, repositoryPath, missingObjectCommit)

	upload := httptest.NewRecorder()
	lfs.Handler{Storage: store}.ServeHTTP(
		upload,
		httptest.NewRequest(
			http.MethodPut,
			"/engineering/assets.git/info/lfs/objects/"+oid,
			bytes.NewReader(content),
		),
	)
	if upload.Code != http.StatusOK {
		t.Fatalf("LFS upload returned %d: %s", upload.Code, upload.Body.String())
	}
	runGit(t, checkout, "push", "origin", "main")

	pointer = fmt.Sprintf(
		"version https://git-lfs.github.com/spec/v1\noid sha256:%s\nsize %d\n",
		oid,
		len(content)+1,
	)
	if err = os.WriteFile(assetPath, []byte(pointer), 0o640); err != nil {
		t.Fatal(err)
	}
	runGit(t, checkout, "add", "media/asset.bin")
	runGit(t, checkout, "commit", "-m", "Write mismatched LFS pointer")
	mismatchedObjectCommit := strings.TrimSpace(runGitOutput(t, checkout, "rev-parse", "HEAD"))
	push = exec.Command("git", "push", "origin", "main")
	push.Dir = checkout
	push.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err = push.CombinedOutput()
	if err == nil {
		t.Fatalf("push with mismatched LFS pointer succeeded:\n%s", output)
	}
	if !strings.Contains(string(output), "LFS object size mismatch") {
		t.Fatalf("push did not report the LFS size mismatch:\n%s", output)
	}
	assertServerObjectMissing(t, store, repositoryPath, mismatchedObjectCommit)
}

func TestNativeGitPushEnforcesRepositoryObjectQuota(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable is not available")
	}
	store := storage.Store{Root: t.TempDir()}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	repositoryPath := repopath.Repository{Groups: []string{"engineering"}, Name: "docs"}
	if err := store.CreateRepository(repositoryPath, storage.CreateRepositoryOptions{
		InitializeReadme: true,
		Author:           "alice",
	}); err != nil {
		t.Fatal(err)
	}
	gitPath, err := store.GitPath(repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	usage, err := directoryRegularFileBytes(filepath.Join(gitPath, "objects"))
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(Handler{
		Storage:                         store,
		MaximumRepositoryGitObjectBytes: usage,
	})
	defer server.Close()

	checkout := filepath.Join(t.TempDir(), "docs")
	runGit(t, "", "clone", server.URL+"/engineering/docs.git", checkout)
	runGit(t, checkout, "config", "user.name", "alice")
	runGit(t, checkout, "config", "user.email", "alice@localhost")
	if err = os.WriteFile(filepath.Join(checkout, "manual.md"), []byte("documentation\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	runGit(t, checkout, "add", "manual.md")
	runGit(t, checkout, "commit", "-m", "Add manual")
	rejectedCommit := strings.TrimSpace(runGitOutput(t, checkout, "rev-parse", "HEAD"))

	push := exec.Command("git", "push", "origin", "main")
	push.Dir = checkout
	push.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := push.CombinedOutput()
	if err == nil {
		t.Fatalf("push exceeding repository quota succeeded:\n%s", output)
	}
	if !strings.Contains(string(output), "repository Git object quota") {
		t.Fatalf("push did not report the repository quota:\n%s", output)
	}
	assertServerObjectMissing(t, store, repositoryPath, rejectedCommit)
}

func TestNativeGitForcePushReclaimsBoundedUnreachablePacks(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable is not available")
	}
	store := storage.Store{Root: t.TempDir()}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	repositoryPath := repopath.Repository{Groups: []string{"engineering"}, Name: "docs"}
	if err := store.CreateRepository(repositoryPath, storage.CreateRepositoryOptions{
		InitializeReadme: true,
		Author:           "alice",
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(Handler{Storage: store})
	defer server.Close()

	checkout := filepath.Join(t.TempDir(), "docs")
	runGit(t, "", "clone", server.URL+"/engineering/docs.git", checkout)
	runGit(t, checkout, "config", "user.name", "alice")
	runGit(t, checkout, "config", "user.email", "alice@localhost")
	base := strings.TrimSpace(runGitOutput(t, checkout, "rev-parse", "HEAD"))
	for index := range automaticMaintenanceMinimumPacks {
		content := fmt.Sprintf("revision %d\n", index)
		if err := os.WriteFile(filepath.Join(checkout, "manual.md"), []byte(content), 0o640); err != nil {
			t.Fatal(err)
		}
		runGit(t, checkout, "add", "manual.md")
		runGit(t, checkout, "commit", "-m", fmt.Sprintf("Revision %d", index))
		runGit(t, checkout, "push", "origin", "main")
	}
	discarded := strings.TrimSpace(runGitOutput(t, checkout, "rev-parse", "HEAD"))
	gitPath, err := store.GitPath(repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	packs, _, err := repositoryPackStats(filepath.Join(gitPath, "objects", "pack"))
	if err != nil {
		t.Fatal(err)
	}
	if packs < automaticMaintenanceMinimumPacks {
		t.Fatalf("server accumulated %d packs, want at least %d", packs, automaticMaintenanceMinimumPacks)
	}

	runGit(t, checkout, "reset", "--hard", base)
	runGit(t, checkout, "push", "--force", "origin", "main")
	assertServerObjectMissing(t, store, repositoryPath, discarded)

	packs, _, err = repositoryPackStats(filepath.Join(gitPath, "objects", "pack"))
	if err != nil {
		t.Fatal(err)
	}
	if packs != 1 {
		t.Fatalf("maintenance left %d packs, want one", packs)
	}
}

func TestNativeGitProtocolV2ClientFallsBackAndClones(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable is not available")
	}
	store := storage.Store{Root: t.TempDir()}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	repositoryPath := repopath.Repository{Groups: []string{"engineering"}, Name: "docs"}
	if err := store.CreateRepository(repositoryPath, storage.CreateRepositoryOptions{
		InitializeReadme: true,
		Author:           "alice",
	}); err != nil {
		t.Fatal(err)
	}

	var sawVersionTwo atomic.Bool
	handler := Handler{Storage: store}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Git-Protocol") == "version=2" {
			sawVersionTwo.Store(true)
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()

	checkout := filepath.Join(t.TempDir(), "docs")
	runGit(
		t,
		"",
		"-c",
		"protocol.version=2",
		"clone",
		server.URL+"/engineering/docs.git",
		checkout,
	)
	if !sawVersionTwo.Load() {
		t.Fatal("Git client did not request protocol version 2")
	}
	if _, err := os.Stat(filepath.Join(checkout, "README.md")); err != nil {
		t.Fatalf("protocol-v2 client clone did not materialize README.md: %v", err)
	}
}

func TestNativeGitPushUsesSelfContainedPack(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable is not available")
	}

	store := storage.Store{Root: t.TempDir()}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	repository := repopath.Repository{Groups: []string{"engineering"}, Name: "docs"}
	if err := store.CreateRepository(repository, storage.CreateRepositoryOptions{
		InitializeReadme: true,
		Author:           "alice",
	}); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(Handler{Storage: store})
	defer server.Close()

	checkout := filepath.Join(t.TempDir(), "docs")
	runGit(t, "", "clone", server.URL+"/engineering/docs.git", checkout)
	runGit(t, checkout, "config", "user.name", "alice")
	runGit(t, checkout, "config", "user.email", "alice@localhost")
	manualPath := filepath.Join(checkout, "manual.md")
	if err := os.WriteFile(manualPath, []byte(strings.Repeat("GitOne documentation line\n", 4096)), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, checkout, "add", "manual.md")
	runGit(t, checkout, "commit", "-m", "Add manual")
	runGit(t, checkout, "push", "origin", "main")

	manual := strings.Repeat("GitOne documentation line\n", 4095) + "Updated documentation line\n"
	if err := os.WriteFile(manualPath, []byte(manual), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, checkout, "add", "manual.md")
	runGit(t, checkout, "commit", "-m", "Update manual")
	runGit(t, checkout, "push", "origin", "main")
}

func TestNativeGitPushNotifiesRepositoryUpdate(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable is not available")
	}
	store := storage.Store{Root: t.TempDir()}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	repositoryPath := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	if err := store.CreateRepository(repositoryPath, storage.CreateRepositoryOptions{
		InitializeReadme: true,
		Author:           "alice",
	}); err != nil {
		t.Fatal(err)
	}
	notifications := make(chan []ReferenceUpdate, 1)
	server := httptest.NewServer(Handler{
		Storage: store,
		RepositoryUpdated: func(repository repopath.Repository, updates []ReferenceUpdate) {
			if repository.Full() != repositoryPath.Full() {
				t.Errorf("notification repository = %q", repository.Full())
			}
			notifications <- updates
		},
	})
	defer server.Close()

	checkout := filepath.Join(t.TempDir(), "api")
	runGit(t, "", "clone", server.URL+"/engineering/api.git", checkout)
	runGit(t, checkout, "config", "user.name", "alice")
	runGit(t, checkout, "config", "user.email", "alice@localhost")
	if err := os.WriteFile(filepath.Join(checkout, "api.go"), []byte("package api\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	runGit(t, checkout, "add", "api.go")
	runGit(t, checkout, "commit", "-m", "Add API")
	expected := strings.TrimSpace(runGitOutput(t, checkout, "rev-parse", "HEAD"))
	runGit(t, checkout, "push", "origin", "main")

	select {
	case updates := <-notifications:
		if len(updates) != 1 ||
			updates[0].Branch != "main" ||
			updates[0].Commit.String() != expected {
			t.Fatalf("unexpected repository updates: %#v", updates)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("repository update notification was not received")
	}
}

func TestNativeGitPushWaitsForRepositoryOperationLock(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable is not available")
	}
	store := storage.Store{Root: t.TempDir()}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	repositoryPath := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	if err := store.CreateRepository(repositoryPath, storage.CreateRepositoryOptions{
		InitializeReadme: true,
		Author:           "alice",
	}); err != nil {
		t.Fatal(err)
	}
	receiveReached := make(chan struct{}, 1)
	server := httptest.NewServer(Handler{
		Storage: store,
		Authorize: func(request *http.Request, _ repopath.Repository, _ bool) (bool, bool) {
			if request.Method == http.MethodPost &&
				strings.HasSuffix(request.URL.Path, "/git-receive-pack") {
				select {
				case receiveReached <- struct{}{}:
				default:
				}
			}
			return true, true
		},
	})
	defer server.Close()

	checkout := filepath.Join(t.TempDir(), "api")
	runGit(t, "", "clone", server.URL+"/engineering/api.git", checkout)
	runGit(t, checkout, "config", "user.name", "alice")
	runGit(t, checkout, "config", "user.email", "alice@localhost")
	if err := os.WriteFile(filepath.Join(checkout, "api.go"), []byte("package api\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	runGit(t, checkout, "add", "api.go")
	runGit(t, checkout, "commit", "-m", "Add API")
	expected := strings.TrimSpace(runGitOutput(t, checkout, "rev-parse", "HEAD"))

	gitPath, err := store.GitPath(repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := git.PlainOpen(gitPath)
	if err != nil {
		t.Fatal(err)
	}
	before, err := repository.Reference(plumbing.NewBranchReferenceName("main"), true)
	if err != nil {
		t.Fatal(err)
	}
	release, err := review.NewStore(store.Root).AcquireOperationLock()
	if err != nil {
		t.Fatal(err)
	}
	type pushResult struct {
		output []byte
		err    error
	}
	pushDone := make(chan pushResult, 1)
	go func() {
		command := exec.Command("git", "push", "origin", "main")
		command.Dir = checkout
		command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		output, pushErr := command.CombinedOutput()
		pushDone <- pushResult{output: output, err: pushErr}
	}()
	select {
	case <-receiveReached:
	case <-time.After(2 * time.Second):
		_ = release()
		t.Fatal("push did not reach receive-pack")
	}
	select {
	case result := <-pushDone:
		_ = release()
		t.Fatalf("push completed while operation lock was held: %v\n%s", result.err, result.output)
	case <-time.After(100 * time.Millisecond):
	}
	during, err := repository.Reference(plumbing.NewBranchReferenceName("main"), true)
	if err != nil {
		_ = release()
		t.Fatal(err)
	}
	if during.Hash() != before.Hash() {
		_ = release()
		t.Fatalf("main changed under operation lock: %s -> %s", before.Hash(), during.Hash())
	}
	if err = release(); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-pushDone:
		if result.err != nil {
			t.Fatalf("push failed after operation lock release: %v\n%s", result.err, result.output)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("push did not resume after operation lock release")
	}
	after, err := repository.Reference(plumbing.NewBranchReferenceName("main"), true)
	if err != nil {
		t.Fatal(err)
	}
	if after.Hash().String() != expected {
		t.Fatalf("main = %s, want %s", after.Hash(), expected)
	}
}

func runGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	_ = runGitOutput(t, directory, args...)
}

func runGitOutput(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	} else {
		return string(output)
	}
	return ""
}

func assertServerObjectMissing(
	t *testing.T,
	store storage.Store,
	repositoryPath repopath.Repository,
	hash string,
) {
	t.Helper()
	gitPath, err := store.GitPath(repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := git.PlainOpen(gitPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repository.Storer.EncodedObject(
		plumbing.AnyObject,
		plumbing.NewHash(hash),
	); !errors.Is(err, plumbing.ErrObjectNotFound) {
		t.Fatalf("rejected object %s remains in live storage: %v", hash, err)
	}

	entries, err := os.ReadDir(filepath.Dir(gitPath))
	if err != nil {
		t.Fatal(err)
	}
	prefix := "." + filepath.Base(gitPath) + ".receive-"
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) {
			t.Fatalf("receive quarantine was not removed: %s", entry.Name())
		}
	}
}
