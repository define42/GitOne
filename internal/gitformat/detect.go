package gitformat

import (
	"fmt"
	"os"
	"path/filepath"

	formatcfg "github.com/go-git/go-git/v6/plumbing/format/config"
)

// DetectObjectFormat reads only the repository's on-disk configuration and
// returns its effective object format. It deliberately does not construct a
// go-git repository, object storage, or hasher, so callers can make the FIPS
// policy decision before entering a legacy SHA-1 code path.
func DetectObjectFormat(path string) (formatcfg.ObjectFormat, error) {
	gitDir, err := sourceGitDirectory(path)
	if err != nil {
		return formatcfg.UnsetObjectFormat, err
	}
	return detectObjectFormatInGitDir(gitDir)
}

func detectObjectFormatInGitDir(gitDir string) (_ formatcfg.ObjectFormat, retErr error) {
	configPath := filepath.Join(gitDir, "config")
	file, err := os.Open(configPath)
	if err != nil {
		return formatcfg.UnsetObjectFormat, fmt.Errorf("open Git config: %w", err)
	}
	defer func() {
		if err := file.Close(); err != nil && retErr == nil {
			retErr = fmt.Errorf("close Git config: %w", err)
		}
	}()

	raw := formatcfg.New()
	if err := formatcfg.NewDecoder(file).Decode(raw); err != nil {
		return formatcfg.UnsetObjectFormat, fmt.Errorf("parse Git config: %w", err)
	}

	value := raw.Section("extensions").Options.Get("objectFormat")
	switch formatcfg.ObjectFormat(value) {
	case formatcfg.UnsetObjectFormat, formatcfg.SHA1:
		// The Git object-format transition specification defines an absent
		// extensions.objectFormat value as the legacy SHA-1 format.
		return formatcfg.SHA1, nil
	case formatcfg.SHA256:
		return formatcfg.SHA256, nil
	default:
		return formatcfg.UnsetObjectFormat, fmt.Errorf("unsupported Git object format %q", value)
	}
}
