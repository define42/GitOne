package repoconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestJobConfigValidationAndDefaults(t *testing.T) {
	config := JobConfig{
		Image:    "golang:1.26.5",
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
	if !(JobConfig{}).MatchesBranch("any-branch") {
		t.Fatal("an omitted branch filter should match every branch")
	}
	if err := (Config{Jobs: map[string]JobConfig{"test": config}}).Validate(); err != nil {
		t.Fatalf("valid named job: %v", err)
	}
	if err := (Config{Jobs: map[string]JobConfig{
		"test":  config,
		"build": {Image: "golang:1.26.5", Script: []string{"go build ./..."}, Needs: []string{"test"}},
	}}).Validate(); err != nil {
		t.Fatalf("valid dependency: %v", err)
	}
}

func TestJobConfigRejectsInvalidValues(t *testing.T) {
	valid := func() JobConfig {
		return JobConfig{Image: "alpine:3", Script: []string{"make test"}}
	}
	tests := []struct {
		name   string
		mutate func(*JobConfig)
		want   string
	}{
		{"missing image", func(c *JobConfig) { c.Image = "" }, "image is required"},
		{"option image", func(c *JobConfig) { c.Image = "--privileged" }, "cannot begin"},
		{"missing script", func(c *JobConfig) { c.Script = nil }, "at least one"},
		{"empty command", func(c *JobConfig) { c.Script = []string{" "} }, "command 1"},
		{"invalid branch pattern", func(c *JobConfig) { c.Branches = []string{"["} }, "branch pattern"},
		{"invalid variable", func(c *JobConfig) {
			c.Environment = map[string]string{"BAD-NAME": "value"}
		}, "environment variable"},
		{"reserved variable", func(c *JobConfig) {
			c.Environment = map[string]string{"CI_COMMIT_SHA": "spoofed"}
		}, "reserved"},
		{"negative timeout", func(c *JobConfig) { c.TimeoutSeconds = -1 }, "timeoutSeconds"},
		{"excessive timeout", func(c *JobConfig) {
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

func TestConfigRejectsInvalidJobCollections(t *testing.T) {
	valid := JobConfig{Image: "alpine:3", Script: []string{"true"}}
	for _, test := range []struct {
		name string
		jobs map[string]JobConfig
		want string
	}{
		{name: "invalid name", jobs: map[string]JobConfig{"bad job": valid}, want: "invalid job name"},
		{name: "invalid job", jobs: map[string]JobConfig{"test": {}}, want: `job "test"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := (Config{Jobs: test.jobs}).Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
	many := make(map[string]JobConfig, MaximumJobs+1)
	for index := 0; index <= MaximumJobs; index++ {
		many[fmt.Sprintf("job-%d", index)] = valid
	}
	if err := (Config{Jobs: many}).Validate(); err == nil ||
		!strings.Contains(err.Error(), "maximum") {
		t.Fatalf("excessive jobs error = %v", err)
	}
}

func TestConfigRejectsInvalidJobDependencies(t *testing.T) {
	job := func(needs ...string) JobConfig {
		return JobConfig{Image: "alpine:3", Script: []string{"true"}, Needs: needs}
	}
	for _, test := range []struct {
		name string
		jobs map[string]JobConfig
		want string
	}{
		{
			name: "missing dependency",
			jobs: map[string]JobConfig{"build": job("test")},
			want: "missing job",
		},
		{
			name: "self dependency",
			jobs: map[string]JobConfig{"test": job("test")},
			want: "itself",
		},
		{
			name: "duplicate dependency",
			jobs: map[string]JobConfig{"test": job(), "build": job("test", "test")},
			want: "more than once",
		},
		{
			name: "dependency cycle",
			jobs: map[string]JobConfig{
				"lint":  job("build"),
				"build": job("test"),
				"test":  job("lint"),
			},
			want: "dependency cycle",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := (Config{Jobs: test.jobs}).Validate()
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
		found || config.Description != "" || len(config.Jobs) != 0 {
		t.Fatalf("legacy JSON config = %#v, found=%v, error=%v", config, found, readErr)
	}

	contents := `description: Backend API
jobs:
  test:
    image: golang:1.26.5
    script:
      - go test ./...
    branches:
      - main
    environment:
      CGO_ENABLED: "0"
  release:
    image: golang:1.26.5
    script:
      - go build ./...
    needs:
      - test
    manual: true
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
	if !found || config.Description != "Backend API" || len(config.Jobs) != 2 ||
		config.Jobs["test"].Image != "golang:1.26.5" ||
		len(config.Jobs["test"].Script) != 1 ||
		config.Jobs["test"].Environment["CGO_ENABLED"] != "0" ||
		!config.Jobs["release"].Manual ||
		len(config.Jobs["release"].Needs) != 1 ||
		config.Jobs["release"].Needs[0] != "test" ||
		config.Jobs["release"].TimeoutSeconds != 1200 {
		t.Fatalf("YAML config = %#v, found=%v", config, found)
	}

	if err = os.WriteFile(
		filepath.Join(directory, ".gitone.yaml"),
		[]byte("build:\n  image: alpine:3\n  script: [true]\n"),
		0o640,
	); err != nil {
		t.Fatal(err)
	}
	if _, err = worktree.Add(".gitone.yaml"); err != nil {
		t.Fatal(err)
	}
	legacySchemaCommit, err := worktree.Commit("Unsupported singular build", &git.CommitOptions{
		Author: &object.Signature{
			Name: "alice", Email: "alice@example.com", When: time.Now().UTC(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err = Read(repository, legacySchemaCommit); err == nil || !found ||
		!strings.Contains(err.Error(), "field build not found") {
		t.Fatalf("legacy build schema found=%v error=%v", found, err)
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
