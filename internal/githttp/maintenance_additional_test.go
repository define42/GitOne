package githttp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMaintenanceHandlesBoundariesAndMalformedPacks(t *testing.T) {
	t.Run("missing objects", func(t *testing.T) {
		if err := maintainRepositoryObjects(t.TempDir()); err == nil {
			t.Fatal("missing object directory was accepted")
		}
	})

	t.Run("oversized objects", func(t *testing.T) {
		repository := t.TempDir()
		objects := filepath.Join(repository, "objects")
		if err := os.Mkdir(objects, 0o750); err != nil {
			t.Fatal(err)
		}
		large := filepath.Join(objects, "large")
		file, err := os.Create(large)
		if err != nil {
			t.Fatal(err)
		}
		if err = file.Truncate(automaticMaintenanceMaximumBytes + 1); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err = file.Close(); err != nil {
			t.Fatal(err)
		}
		if err = maintainRepositoryObjects(repository); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("missing pack directory", func(t *testing.T) {
		repository := t.TempDir()
		if err := os.Mkdir(filepath.Join(repository, "objects"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := maintainRepositoryObjects(repository); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("invalid pack directory", func(t *testing.T) {
		repository := t.TempDir()
		objects := filepath.Join(repository, "objects")
		if err := os.Mkdir(objects, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(objects, "pack"), []byte("file"), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := maintainRepositoryObjects(repository); err == nil {
			t.Fatal("invalid pack directory was accepted")
		}
	})

	t.Run("missing pack index", func(t *testing.T) {
		pack := t.TempDir()
		if err := os.WriteFile(filepath.Join(pack, "pack-deadbeef.pack"), []byte("pack"), 0o640); err != nil {
			t.Fatal(err)
		}
		if _, _, err := repositoryPackStats(pack); err == nil {
			t.Fatal("pack without index was accepted")
		}
	})

	for _, test := range []struct {
		name     string
		contents []byte
	}{
		{name: "short header", contents: []byte("short")},
		{name: "unsupported version", contents: []byte{0xff, 0x74, 0x4f, 0x63, 0, 0, 0, 3}},
		{name: "short fanout", contents: make([]byte, 8)},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "pack.idx")
			if err := os.WriteFile(path, test.contents, 0o640); err != nil {
				t.Fatal(err)
			}
			if _, err := packIndexObjectCount(path); err == nil {
				t.Fatal("malformed pack index was accepted")
			}
		})
	}
}
