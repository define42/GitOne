package repoconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

const (
	DefaultTimeoutSeconds = 900
	MaximumTimeoutSeconds = 3600
	maximumConfigBytes    = 1 << 20
)

var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Config is the repository-owned configuration stored in .gitone.json.
type Config struct {
	Description string       `json:"description"`
	Build       *BuildConfig `json:"build,omitempty"`
}

// BuildConfig describes an isolated container build.
type BuildConfig struct {
	Image          string            `json:"image"`
	Script         []string          `json:"script"`
	Branches       []string          `json:"branches,omitempty"`
	Environment    map[string]string `json:"environment,omitempty"`
	TimeoutSeconds int               `json:"timeoutSeconds,omitempty"`
}

// Read loads .gitone.json from one exact commit. A missing file is not an error.
func Read(repository *git.Repository, revision plumbing.Hash) (Config, bool, error) {
	commit, err := repository.CommitObject(revision)
	if err != nil {
		return Config{}, false, err
	}
	file, err := commit.File(".gitone.json")
	if errors.Is(err, object.ErrFileNotFound) {
		return Config{}, false, nil
	}
	if err != nil {
		return Config{}, false, err
	}
	if file.Size > maximumConfigBytes {
		return Config{}, true, fmt.Errorf(".gitone.json exceeds %d bytes", maximumConfigBytes)
	}
	contents, err := file.Contents()
	if err != nil {
		return Config{}, false, err
	}
	var config Config
	if err = json.Unmarshal([]byte(contents), &config); err != nil {
		return Config{}, true, fmt.Errorf("read .gitone.json: %w", err)
	}
	return config, true, nil
}

func (b BuildConfig) Validate() error {
	if strings.TrimSpace(b.Image) == "" {
		return errors.New("build image is required")
	}
	if strings.HasPrefix(strings.TrimSpace(b.Image), "-") {
		return errors.New("build image cannot begin with '-'")
	}
	if len(b.Script) == 0 {
		return errors.New("build script must contain at least one command")
	}
	for index, command := range b.Script {
		if strings.TrimSpace(command) == "" {
			return fmt.Errorf("build script command %d is empty", index+1)
		}
	}
	for _, pattern := range b.Branches {
		if strings.TrimSpace(pattern) == "" {
			return errors.New("build branch pattern cannot be empty")
		}
		if _, err := path.Match(pattern, "main"); err != nil {
			return fmt.Errorf("invalid build branch pattern %q: %w", pattern, err)
		}
	}
	for name := range b.Environment {
		if !environmentName.MatchString(name) {
			return fmt.Errorf("invalid build environment variable %q", name)
		}
		if strings.HasPrefix(name, "GITONE_") || strings.HasPrefix(name, "CI_") {
			return fmt.Errorf("build environment variable %q is reserved", name)
		}
	}
	if b.TimeoutSeconds < 0 || b.TimeoutSeconds > MaximumTimeoutSeconds {
		return fmt.Errorf(
			"build timeoutSeconds must be between 0 and %d",
			MaximumTimeoutSeconds,
		)
	}
	return nil
}

func (b BuildConfig) Timeout() int {
	if b.TimeoutSeconds == 0 {
		return DefaultTimeoutSeconds
	}
	return b.TimeoutSeconds
}

func (b BuildConfig) MatchesBranch(branch string) bool {
	if len(b.Branches) == 0 {
		return true
	}
	for _, pattern := range b.Branches {
		if matches, err := path.Match(pattern, branch); err == nil && matches {
			return true
		}
	}
	return false
}
