package storage

import (
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

func FuzzArchiveEntryPathRemainsWithinRoot(f *testing.F) {
	for _, name := range []string{
		"repository.git/HEAD",
		"folder/../file",
		"../outside",
		"/absolute",
		`folder\file`,
		"bad\x00name",
		"",
	} {
		f.Add(name)
	}
	f.Fuzz(func(t *testing.T, name string) {
		root, err := filepath.Abs(filepath.Join(os.TempDir(), "gitone-archive-fuzz-root"))
		if err != nil {
			t.Fatal(err)
		}
		target, cleanName, err := archiveEntryPath(root, name)
		if err != nil {
			return
		}
		if cleanName != path.Clean(name) ||
			path.IsAbs(cleanName) ||
			cleanName == ".." ||
			strings.HasPrefix(cleanName, "../") {
			t.Fatalf("accepted unsafe archive name %q as %q", name, cleanName)
		}
		relative, err := filepath.Rel(root, target)
		if err != nil {
			t.Fatal(err)
		}
		if relative == ".." ||
			strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			t.Fatalf("archive name %q escaped root to %q", name, target)
		}
	})
}
