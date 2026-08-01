package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/define42/GitOne/internal/gitformat"
	git "github.com/go-git/go-git/v6"
	formatcfg "github.com/go-git/go-git/v6/plumbing/format/config"
)

// normalizeImportedRepository moves an already-SHA-256 repository, or
// rewrites a legacy SHA-1 repository, into a fresh SHA-256-only destination.
// sourcePath and destinationPath must both be private staging paths.
func normalizeImportedRepository(
	sourcePath string,
	destinationPath string,
) (*git.Repository, error) {
	if filepath.Clean(sourcePath) == filepath.Clean(destinationPath) {
		return nil, errors.New("source and destination repository paths must differ")
	}
	objectFormat, err := gitformat.DetectObjectFormat(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("inspect imported repository object format: %w", err)
	}

	switch objectFormat {
	case formatcfg.SHA256:
		source, openErr := gitformat.Open(sourcePath)
		if openErr != nil {
			return nil, fmt.Errorf("open imported SHA-256 repository: %w", openErr)
		}
		repository, copyErr := gitformat.CopySHA256Repository(source, destinationPath)
		closeErr := source.Close()
		if copyErr != nil {
			return nil, fmt.Errorf("copy imported SHA-256 repository: %w", copyErr)
		}
		if closeErr != nil {
			_ = repository.Close()
			_ = os.RemoveAll(destinationPath)
			return nil, fmt.Errorf("close imported SHA-256 repository: %w", closeErr)
		}
		if err = os.RemoveAll(sourcePath); err != nil {
			_ = repository.Close()
			return nil, fmt.Errorf("remove staged SHA-256 repository: %w", err)
		}
		return repository, nil

	case formatcfg.SHA1:
		if err = gitformat.RequireLegacySHA1(); err != nil {
			return nil, fmt.Errorf("inspect imported SHA-1 repository: %w", err)
		}
		repository, convertErr := gitformat.ConvertSHA1Repository(
			sourcePath,
			destinationPath,
		)
		if convertErr != nil {
			return nil, fmt.Errorf("convert imported SHA-1 repository: %w", convertErr)
		}
		if err = os.RemoveAll(sourcePath); err != nil {
			_ = repository.Close()
			return nil, fmt.Errorf("remove staged SHA-1 repository: %w", err)
		}
		return repository, nil

	default:
		return nil, fmt.Errorf("unsupported imported Git object format %q", objectFormat)
	}
}
