package githttp

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/define42/GitOne/internal/repopath"
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

func TestControlRejectsTags(t *testing.T) {
	req := packp.NewReferenceUpdateRequest()
	req.Commands = []*packp.Command{{Name: plumbing.NewTagReferenceName("v1"), Old: plumbing.NewHash("1111111111111111111111111111111111111111"), New: plumbing.NewHash("2222222222222222222222222222222222222222")}}
	if e := validateControlRefs(req); e == nil {
		t.Fatal("expected rejection")
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

func runGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}
