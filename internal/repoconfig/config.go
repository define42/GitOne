package repoconfig

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"gopkg.in/yaml.v3"
)

const (
	FileName              = ".gitone.yaml"
	DefaultTimeoutSeconds = 900
	MaximumTimeoutSeconds = 3600
	MaximumJobs           = 128
	maximumConfigBytes    = 1 << 20
)

var (
	environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	jobName         = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
)

// Config is the repository-owned configuration stored in .gitone.yaml.
type Config struct {
	Description string               `json:"description" yaml:"description"`
	Jobs        map[string]JobConfig `json:"jobs,omitempty" yaml:"jobs,omitempty"`
}

// JobConfig describes one named isolated container build job.
type JobConfig struct {
	Image          string            `json:"image" yaml:"image"`
	Script         []string          `json:"script" yaml:"script"`
	Needs          []string          `json:"needs,omitempty" yaml:"needs,omitempty"`
	Manual         bool              `json:"manual,omitempty" yaml:"manual,omitempty"`
	Branches       []string          `json:"branches,omitempty" yaml:"branches,omitempty"`
	Environment    map[string]string `json:"environment,omitempty" yaml:"environment,omitempty"`
	TimeoutSeconds int               `json:"timeoutSeconds,omitempty" yaml:"timeoutSeconds,omitempty"`
}

// Read loads .gitone.yaml from one exact commit. A missing file is not an error.
func Read(repository *git.Repository, revision plumbing.Hash) (Config, bool, error) {
	commit, err := repository.CommitObject(revision)
	if err != nil {
		return Config{}, false, err
	}
	file, err := commit.File(FileName)
	if errors.Is(err, object.ErrFileNotFound) {
		return Config{}, false, nil
	}
	if err != nil {
		return Config{}, false, err
	}
	if file.Size > maximumConfigBytes {
		return Config{}, true, fmt.Errorf("%s exceeds %d bytes", FileName, maximumConfigBytes)
	}
	contents, err := file.Contents()
	if err != nil {
		return Config{}, false, err
	}
	var config Config
	decoder := yaml.NewDecoder(strings.NewReader(contents))
	decoder.KnownFields(true)
	if err = decoder.Decode(&config); err != nil {
		return Config{}, true, fmt.Errorf("read %s: %w", FileName, err)
	}
	return config, true, nil
}

// Validate checks every named job before any of them are scheduled.
func (c Config) Validate() error {
	if len(c.Jobs) > MaximumJobs {
		return fmt.Errorf("configuration has %d jobs; maximum is %d", len(c.Jobs), MaximumJobs)
	}
	names := make([]string, 0, len(c.Jobs))
	for name := range c.Jobs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		job := c.Jobs[name]
		if !jobName.MatchString(name) {
			return fmt.Errorf(
				"invalid job name %q; use 1-100 letters, numbers, dots, underscores, or hyphens",
				name,
			)
		}
		if err := job.Validate(); err != nil {
			return fmt.Errorf("job %q: %w", name, err)
		}
		seenNeeds := make(map[string]struct{}, len(job.Needs))
		for _, need := range job.Needs {
			if !jobName.MatchString(need) {
				return fmt.Errorf("job %q has invalid dependency name %q", name, need)
			}
			if need == name {
				return fmt.Errorf("job %q cannot depend on itself", name)
			}
			if _, found := c.Jobs[need]; !found {
				return fmt.Errorf("job %q depends on missing job %q", name, need)
			}
			if _, duplicate := seenNeeds[need]; duplicate {
				return fmt.Errorf("job %q lists dependency %q more than once", name, need)
			}
			seenNeeds[need] = struct{}{}
		}
	}
	states := make(map[string]uint8, len(c.Jobs))
	stack := make([]string, 0, len(c.Jobs))
	var visit func(string) error
	visit = func(name string) error {
		if states[name] == 2 {
			return nil
		}
		if states[name] == 1 {
			start := 0
			for index, entry := range stack {
				if entry == name {
					start = index
					break
				}
			}
			cycle := append(append([]string{}, stack[start:]...), name)
			return fmt.Errorf("job dependency cycle: %s", strings.Join(cycle, " -> "))
		}
		states[name] = 1
		stack = append(stack, name)
		for _, need := range c.Jobs[name].Needs {
			if err := visit(need); err != nil {
				return err
			}
		}
		stack = stack[:len(stack)-1]
		states[name] = 2
		return nil
	}
	for _, name := range names {
		if err := visit(name); err != nil {
			return err
		}
	}
	return nil
}

func (b JobConfig) Validate() error {
	if strings.TrimSpace(b.Image) == "" {
		return errors.New("job image is required")
	}
	if strings.HasPrefix(strings.TrimSpace(b.Image), "-") {
		return errors.New("job image cannot begin with '-'")
	}
	if len(b.Script) == 0 {
		return errors.New("job script must contain at least one command")
	}
	for index, command := range b.Script {
		if strings.TrimSpace(command) == "" {
			return fmt.Errorf("job script command %d is empty", index+1)
		}
	}
	for _, pattern := range b.Branches {
		if strings.TrimSpace(pattern) == "" {
			return errors.New("job branch pattern cannot be empty")
		}
		if _, err := path.Match(pattern, "main"); err != nil {
			return fmt.Errorf("invalid job branch pattern %q: %w", pattern, err)
		}
	}
	for name := range b.Environment {
		if !environmentName.MatchString(name) {
			return fmt.Errorf("invalid job environment variable %q", name)
		}
		if strings.HasPrefix(name, "GITONE_") || strings.HasPrefix(name, "CI_") {
			return fmt.Errorf("job environment variable %q is reserved", name)
		}
	}
	if b.TimeoutSeconds < 0 || b.TimeoutSeconds > MaximumTimeoutSeconds {
		return fmt.Errorf(
			"job timeoutSeconds must be between 0 and %d",
			MaximumTimeoutSeconds,
		)
	}
	return nil
}

func (b JobConfig) Timeout() int {
	if b.TimeoutSeconds == 0 {
		return DefaultTimeoutSeconds
	}
	return b.TimeoutSeconds
}

func (b JobConfig) MatchesBranch(branch string) bool {
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
