// Package gitformat provides the SHA-256-only repository boundary used by
// GitOne and a one-way converter for legacy SHA-1 repositories.
package gitformat

import (
	"errors"
	"fmt"
	"strings"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	formatcfg "github.com/go-git/go-git/v6/plumbing/format/config"
)

var (
	// ErrNotSHA256Repository is returned when a repository is not configured
	// exclusively for Git's SHA-256 object format.
	ErrNotSHA256Repository = errors.New("repository is not SHA-256")
	// ErrLegacySHA1Unavailable is returned when Go's strict FIPS-only
	// diagnostic mode disables the SHA-1 calculation needed to validate a
	// legacy repository during conversion.
	ErrLegacySHA1Unavailable = errors.New("legacy SHA-1 conversion is unavailable")
)

// Init creates a repository that exclusively uses Git's SHA-256 object
// format. The path and bare flag have the same meaning as git.PlainInit.
func Init(path string, bare bool) (*git.Repository, error) {
	repo, err := git.PlainInit(path, bare, git.WithObjectFormat(formatcfg.SHA256))
	if err != nil {
		return nil, err
	}
	if err := Validate(repo); err != nil {
		_ = repo.Close()
		return nil, err
	}
	return repo, nil
}

// Open opens path and rejects it unless it is a SHA-256 repository.
func Open(path string) (*git.Repository, error) {
	objectFormat, err := DetectObjectFormat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect repository object format: %w", err)
	}
	if objectFormat != formatcfg.SHA256 {
		return nil, fmt.Errorf(
			"%w: require extensions.objectFormat=sha256",
			ErrNotSHA256Repository,
		)
	}

	repo, err := git.PlainOpen(path)
	if err != nil {
		return nil, err
	}
	if err := Validate(repo); err != nil {
		_ = repo.Close()
		return nil, err
	}
	return repo, nil
}

// ObjectFormat returns the effective object format. Git repositories that do
// not declare extensions.objectFormat have the legacy SHA-1 format.
func ObjectFormat(repo *git.Repository) (formatcfg.ObjectFormat, error) {
	if repo == nil {
		return formatcfg.UnsetObjectFormat, errors.New("nil repository")
	}
	cfg, err := repo.Config()
	if err != nil {
		return formatcfg.UnsetObjectFormat, err
	}
	switch cfg.Extensions.ObjectFormat {
	case formatcfg.UnsetObjectFormat, formatcfg.SHA1:
		return formatcfg.SHA1, nil
	case formatcfg.SHA256:
		return formatcfg.SHA256, nil
	default:
		return formatcfg.UnsetObjectFormat, fmt.Errorf(
			"unsupported Git object format %q", cfg.Extensions.ObjectFormat,
		)
	}
}

// Validate verifies the inexpensive invariants of a live GitOne repository:
// repository format version 1, SHA-256 objects, no compatibility object
// format, and correctly sized references. It deliberately does not perform a
// full object graph fsck on every open.
func Validate(repo *git.Repository) error {
	if repo == nil {
		return errors.New("nil repository")
	}
	cfg, err := repo.Config()
	if err != nil {
		return err
	}
	objectFormat, err := ObjectFormat(repo)
	if err != nil {
		return err
	}
	if objectFormat != formatcfg.SHA256 ||
		cfg.Extensions.ObjectFormat != formatcfg.SHA256 ||
		cfg.Core.RepositoryFormatVersion != formatcfg.Version1 {
		return fmt.Errorf(
			"%w: require core.repositoryFormatVersion=1 and extensions.objectFormat=sha256",
			ErrNotSHA256Repository,
		)
	}
	if cfg.Raw != nil && cfg.Raw.HasSection("extensions") {
		for _, option := range cfg.Raw.Section("extensions").Options {
			if strings.EqualFold(option.Key, "compatObjectFormat") {
				return fmt.Errorf("%w: extensions.compatObjectFormat is forbidden", ErrNotSHA256Repository)
			}
		}
	}

	refs, err := repo.References()
	if err != nil {
		return err
	}
	return refs.ForEach(func(ref *plumbing.Reference) error {
		if err := ref.Name().Validate(); err != nil {
			return fmt.Errorf("invalid reference %q: %w", ref.Name(), err)
		}
		switch ref.Type() {
		case plumbing.HashReference:
			if ref.Hash().IsZero() || !IsSHA256OID(ref.Hash().String()) {
				return fmt.Errorf(
					"%w: reference %q has non-SHA-256 object ID %q",
					ErrNotSHA256Repository, ref.Name(), ref.Hash(),
				)
			}
		case plumbing.SymbolicReference:
			if err := ref.Target().Validate(); err != nil {
				return fmt.Errorf("reference %q has invalid symbolic target: %w", ref.Name(), err)
			}
		default:
			return fmt.Errorf("reference %q has unsupported type %s", ref.Name(), ref.Type())
		}
		return nil
	})
}

// IsSHA256OID reports whether value is a canonical, full-length Git SHA-256
// object ID. Canonical object IDs use exactly 64 lowercase hexadecimal bytes.
func IsSHA256OID(value string) bool {
	if len(value) != formatcfg.SHA256HexSize {
		return false
	}
	for i := range len(value) {
		c := value[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
