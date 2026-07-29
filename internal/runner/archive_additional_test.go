package runner

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/define42/GitOne/internal/repoconfig"
	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/storage"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

type archiveTestEntry struct {
	header  tar.Header
	content string
}

func TestExtractSourceArchiveFilesAndSymlinks(t *testing.T) {
	contents := sourceArchive(t,
		archiveTestEntry{
			header:  tar.Header{Name: "bin/run.sh", Typeflag: tar.TypeReg, Mode: 0o755},
			content: "#!/bin/sh\n",
		},
		archiveTestEntry{
			header: tar.Header{
				Name: "run", Typeflag: tar.TypeSymlink, Mode: 0o777, Linkname: "bin/run.sh",
			},
		},
	)
	destination := t.TempDir()
	if err := ExtractSourceArchive(bytes.NewReader(contents), destination); err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(filepath.Join(destination, "bin", "run.sh"))
	if err != nil || string(script) != "#!/bin/sh\n" {
		t.Fatalf("script = %q, %v", script, err)
	}
	info, err := os.Stat(filepath.Join(destination, "bin", "run.sh"))
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("script mode = %v, %v", info.Mode(), err)
	}
	link, err := os.Readlink(filepath.Join(destination, "run"))
	if err != nil || link != "bin/run.sh" {
		t.Fatalf("symlink = %q, %v", link, err)
	}
}

func TestExtractSourceArchiveRejectsUnsafeAndUnsupportedEntries(t *testing.T) {
	tests := []struct {
		name    string
		entry   archiveTestEntry
		message string
	}{
		{
			name: "parent traversal",
			entry: archiveTestEntry{
				header: tar.Header{Name: "../outside", Typeflag: tar.TypeReg, Mode: 0o644},
			},
			message: "unsafe source archive path",
		},
		{
			name: "unclean path",
			entry: archiveTestEntry{
				header: tar.Header{Name: "dir/../outside", Typeflag: tar.TypeReg, Mode: 0o644},
			},
			message: "unsafe source archive path",
		},
		{
			name: "absolute symlink",
			entry: archiveTestEntry{
				header: tar.Header{
					Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd",
				},
			},
			message: "unsafe absolute source symlink",
		},
		{
			name: "escaping symlink",
			entry: archiveTestEntry{
				header: tar.Header{
					Name: "link", Typeflag: tar.TypeSymlink, Linkname: "../outside",
				},
			},
			message: "unsafe source symlink",
		},
		{
			name: "directory",
			entry: archiveTestEntry{
				header: tar.Header{Name: "directory", Typeflag: tar.TypeDir, Mode: 0o755},
			},
			message: "unsupported source archive entry",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ExtractSourceArchive(
				bytes.NewReader(sourceArchive(t, test.entry)),
				t.TempDir(),
			)
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("error = %v, want containing %q", err, test.message)
			}
		})
	}
}

func TestExtractSourceArchiveReportsInputAndFilesystemErrors(t *testing.T) {
	t.Run("invalid gzip", func(t *testing.T) {
		if err := ExtractSourceArchive(strings.NewReader("not gzip"), t.TempDir()); err == nil {
			t.Fatal("invalid gzip archive was accepted")
		}
	})

	t.Run("invalid tar", func(t *testing.T) {
		var contents bytes.Buffer
		compressed := gzip.NewWriter(&contents)
		if _, err := compressed.Write([]byte("incomplete tar")); err != nil {
			t.Fatal(err)
		}
		if err := compressed.Close(); err != nil {
			t.Fatal(err)
		}
		if err := ExtractSourceArchive(&contents, t.TempDir()); err == nil {
			t.Fatal("invalid tar archive was accepted")
		}
	})

	t.Run("destination is a file", func(t *testing.T) {
		destination := filepath.Join(t.TempDir(), "destination")
		if err := os.WriteFile(destination, []byte("occupied"), 0o640); err != nil {
			t.Fatal(err)
		}
		contents := sourceArchive(t, archiveTestEntry{
			header: tar.Header{Name: "source.txt", Typeflag: tar.TypeReg, Mode: 0o644},
		})
		if err := ExtractSourceArchive(bytes.NewReader(contents), destination); err == nil {
			t.Fatal("file destination was accepted")
		}
	})
}

func TestWriteSourceArchivePreservesExecutableAndSymlink(t *testing.T) {
	root := t.TempDir()
	repositoryPath := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	store := storage.Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRepository(repositoryPath, storage.CreateRepositoryOptions{
		InitializeReadme: true,
		Author:           "alice",
	}); err != nil {
		t.Fatal(err)
	}
	_ = commitBuildConfig(t, store, repositoryPath, repoconfig.Config{
		Build: &repoconfig.BuildConfig{Image: "alpine:3", Script: []string{"true"}},
	})

	gitPath, err := store.GitPath(repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(t.TempDir(), "checkout")
	repository, err := git.PlainClone(checkout, false, &git.CloneOptions{URL: gitPath})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(checkout, "bin"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(
		filepath.Join(checkout, "bin", "run.sh"),
		[]byte("#!/bin/sh\n"),
		0o755,
	); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink("run.sh", filepath.Join(checkout, "bin", "run")); err != nil {
		t.Fatal(err)
	}
	worktree, err := repository.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bin/run.sh", "bin/run"} {
		if _, err = worktree.Add(name); err != nil {
			t.Fatal(err)
		}
	}
	commit, err := worktree.Commit("Add executable and symlink", &git.CommitOptions{
		Author: &object.Signature{
			Name: "alice", Email: "alice@example.com", When: time.Now().UTC(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = repository.Push(&git.PushOptions{}); err != nil {
		t.Fatal(err)
	}

	var contents bytes.Buffer
	if err = WriteSourceArchive(store, repositoryPath, commit, &contents); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if err = ExtractSourceArchive(&contents, destination); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(destination, "bin", "run.sh"))
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("executable mode = %v, %v", info.Mode(), err)
	}
	link, err := os.Readlink(filepath.Join(destination, "bin", "run"))
	if err != nil || link != "run.sh" {
		t.Fatalf("archived symlink = %q, %v", link, err)
	}
}

func TestWriteSourceArchiveReportsRepositoryAndOutputErrors(t *testing.T) {
	root := t.TempDir()
	store := storage.Store{Root: root}
	validRepository := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}

	if err := WriteSourceArchive(
		store,
		repopath.Repository{Groups: []string{".."}, Name: "api"},
		plumbing.ZeroHash,
		io.Discard,
	); err == nil {
		t.Fatal("unsafe repository path was accepted")
	}
	if err := WriteSourceArchive(
		store,
		validRepository,
		plumbing.ZeroHash,
		io.Discard,
	); err == nil {
		t.Fatal("missing repository was accepted")
	}

	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRepository(validRepository, storage.CreateRepositoryOptions{
		InitializeReadme: true, Author: "alice",
	}); err != nil {
		t.Fatal(err)
	}
	if err := WriteSourceArchive(
		store,
		validRepository,
		plumbing.NewHash(strings.Repeat("f", 40)),
		io.Discard,
	); err == nil {
		t.Fatal("unknown commit was accepted")
	}
	gitPath, err := store.GitPath(validRepository)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := git.PlainOpen(gitPath)
	if err != nil {
		t.Fatal(err)
	}
	head, err := repository.Head()
	if err != nil {
		t.Fatal(err)
	}
	if err = WriteSourceArchive(
		store,
		validRepository,
		head.Hash(),
		alwaysErrorWriter{},
	); err == nil {
		t.Fatal("output failure was ignored")
	}
}

type alwaysErrorWriter struct{}

func (alwaysErrorWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

func sourceArchive(t *testing.T, entries ...archiveTestEntry) []byte {
	t.Helper()
	var contents bytes.Buffer
	compressed := gzip.NewWriter(&contents)
	archive := tar.NewWriter(compressed)
	for _, entry := range entries {
		header := entry.header
		if header.Typeflag == tar.TypeReg {
			header.Size = int64(len(entry.content))
		}
		if err := archive.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if entry.content != "" {
			if _, err := io.WriteString(archive, entry.content); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	return contents.Bytes()
}
