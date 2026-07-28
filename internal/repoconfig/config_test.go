package repoconfig

import (
	"strings"
	"testing"
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
