package githttp

import (
	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/storage"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
	if err := os.WriteFile(manualPath, []byte(strings.Repeat("GitOne documentation line\n", 4096)), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, checkout, "add", "manual.md")
	runGit(t, checkout, "commit", "-m", "Add manual")
	runGit(t, checkout, "push", "origin", "main")

	manual := strings.Repeat("GitOne documentation line\n", 4095) + "Updated documentation line\n"
	if err := os.WriteFile(manualPath, []byte(manual), 0644); err != nil {
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
