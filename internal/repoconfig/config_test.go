package repoconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestBuildConfigValidationAndDefaults(t *testing.T) {
	config := BuildConfig{
		Image:    "golang:1.25",
		Script:   []string{"go test ./...", "go build ./..."},
		Branches: []string{"main", "release/*"},
		Environment: map[string]string{
			"CGO_ENABLED": "0",
		},
	}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	if config.Timeout() != DefaultTimeoutSeconds {
		t.Fatalf("default timeout = %d", config.Timeout())
	}
	for _, branch := range []string{"main", "release/1.0"} {
		if !config.MatchesBranch(branch) {
			t.Fatalf("expected branch %q to match", branch)
		}
	}
	if config.MatchesBranch("feature/docs") {
		t.Fatal("unexpected feature branch match")
	}
	if !(BuildConfig{}).MatchesBranch("any-branch") {
		t.Fatal("an omitted branch filter should match every branch")
	}
}

func TestBuildConfigRejectsInvalidValues(t *testing.T) {
	valid := func() BuildConfig {
		return BuildConfig{Image: "alpine:3", Script: []string{"make test"}}
	}
	tests := []struct {
		name   string
		mutate func(*BuildConfig)
		want   string
	}{
		{"missing image", func(c *BuildConfig) { c.Image = "" }, "image is required"},
		{"option image", func(c *BuildConfig) { c.Image = "--privileged" }, "cannot begin"},
		{"missing script", func(c *BuildConfig) { c.Script = nil }, "at least one"},
		{"empty command", func(c *BuildConfig) { c.Script = []string{" "} }, "command 1"},
		{"invalid branch pattern", func(c *BuildConfig) { c.Branches = []string{"["} }, "branch pattern"},
		{"invalid variable", func(c *BuildConfig) {
			c.Environment = map[string]string{"BAD-NAME": "value"}
		}, "environment variable"},
		{"reserved variable", func(c *BuildConfig) {
			c.Environment = map[string]string{"CI_COMMIT_SHA": "spoofed"}
		}, "reserved"},
		{"negative timeout", func(c *BuildConfig) { c.TimeoutSeconds = -1 }, "timeoutSeconds"},
		{"excessive timeout", func(c *BuildConfig) {
			c.TimeoutSeconds = MaximumTimeoutSeconds + 1
		}, "timeoutSeconds"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := valid()
			test.mutate(&config)
			err := config.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestReadUsesOnlyGitOneYAML(t *testing.T) {
	directory := t.TempDir()
	repository, err := git.PlainInit(directory, false)
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := repository.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(
		filepath.Join(directory, ".gitone.json"),
		[]byte(`{"description":"legacy"}`),
		0o640,
	); err != nil {
		t.Fatal(err)
	}
	if _, err = worktree.Add(".gitone.json"); err != nil {
		t.Fatal(err)
	}
	legacyCommit, err := worktree.Commit("Legacy JSON configuration", &git.CommitOptions{
		Author: &object.Signature{
			Name: "alice", Email: "alice@example.com", When: time.Now().UTC(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if config, found, readErr := Read(repository, legacyCommit); readErr != nil ||
		found || config != (Config{}) {
		t.Fatalf("legacy JSON config = %#v, found=%v, error=%v", config, found, readErr)
	}

	contents := `description: Backend API
build:
  image: golang:1.25
  script:
    - go test ./...
  manual: true
  branches:
    - main
  environment:
    CGO_ENABLED: "0"
  timeoutSeconds: 1200
`
	if err = os.WriteFile(
		filepath.Join(directory, ".gitone.yaml"),
		[]byte(contents),
		0o640,
	); err != nil {
		t.Fatal(err)
	}
	if _, err = worktree.Add(".gitone.yaml"); err != nil {
		t.Fatal(err)
	}
	yamlCommit, err := worktree.Commit("YAML configuration", &git.CommitOptions{
		Author: &object.Signature{
			Name: "alice", Email: "alice@example.com", When: time.Now().UTC(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	config, found, err := Read(repository, yamlCommit)
	if err != nil {
		t.Fatal(err)
	}
	if !found || config.Description != "Backend API" || config.Build == nil ||
		config.Build.Image != "golang:1.25" ||
		len(config.Build.Script) != 1 ||
		!config.Build.Manual ||
		config.Build.Environment["CGO_ENABLED"] != "0" ||
		config.Build.TimeoutSeconds != 1200 {
		t.Fatalf("YAML config = %#v, found=%v", config, found)
	}

	if err = os.WriteFile(
		filepath.Join(directory, ".gitone.yaml"),
		[]byte("build: ["),
		0o640,
	); err != nil {
		t.Fatal(err)
	}
	if _, err = worktree.Add(".gitone.yaml"); err != nil {
		t.Fatal(err)
	}
	invalidCommit, err := worktree.Commit("Invalid YAML configuration", &git.CommitOptions{
		Author: &object.Signature{
			Name: "alice", Email: "alice@example.com", When: time.Now().UTC(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err = Read(repository, invalidCommit); err == nil || !found ||
		!strings.Contains(err.Error(), "read .gitone.yaml") {
		t.Fatalf("invalid YAML found=%v error=%v", found, err)
	}
}
