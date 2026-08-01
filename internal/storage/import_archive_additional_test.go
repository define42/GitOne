package storage

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/define42/GitOne/internal/gitformat"
	"github.com/define42/GitOne/internal/repopath"
	git "github.com/go-git/go-git/v6"
)

func TestArchiveImportErrorMethods(t *testing.T) {
	cause := errors.New("broken archive")
	err := &ArchiveImportError{Err: cause}
	if err.Error() != "invalid repository archive: broken archive" {
		t.Fatalf("Error() = %q", err.Error())
	}
	if !errors.Is(err, cause) {
		t.Fatal("archive import error did not unwrap its cause")
	}
}

func TestImportRepositoryArchiveLockedAndPlainTAR(t *testing.T) {
	sourceRoot := t.TempDir()
	sourceStore := Store{Root: sourceRoot}
	if err := sourceStore.CreateGroup("source", "alice", ""); err != nil {
		t.Fatal(err)
	}
	source := repopath.Repository{Groups: []string{"source"}, Name: "api"}
	if err := sourceStore.CreateRepository(source, CreateRepositoryOptions{
		InitializeReadme: true,
		Author:           "alice",
	}); err != nil {
		t.Fatal(err)
	}
	sourcePath, err := sourceStore.GitPath(source)
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(t.TempDir(), "api.tar")
	writeRepositoryTAR(t, sourcePath, archivePath, "api.git")

	root := t.TempDir()
	store := Store{Root: root}
	if err = store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	target := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	if err = store.ImportRepositoryArchiveLocked(
		context.Background(),
		target,
		"api.tar",
		archivePath,
	); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(root, "engineering", "api.lfs", "objects")); err != nil {
		t.Fatal(err)
	}
	if err = store.ImportRepositoryArchiveLocked(
		context.Background(),
		repopath.Repository{Groups: []string{"engineering"}, Name: "control"},
		"api.tar",
		archivePath,
	); err == nil {
		t.Fatal("locked archive import accepted the reserved control repository")
	}
	rejected := errors.New("authorization changed")
	if err = store.ImportRepositoryArchiveValidated(
		context.Background(),
		repopath.Repository{Groups: []string{"engineering"}, Name: "rejected"},
		"api.tar",
		archivePath,
		func() error { return rejected },
	); !errors.Is(err, rejected) {
		t.Fatalf("validated archive import error = %v", err)
	}
	if err = store.ImportRepositoryArchiveValidated(
		context.Background(),
		repopath.Repository{Groups: []string{"engineering"}, Name: "control"},
		"api.tar",
		archivePath,
		nil,
	); err == nil {
		t.Fatal("validated archive import accepted the reserved control repository")
	}
	if err = store.importRepositoryArchive(
		context.Background(),
		repopath.Repository{Groups: []string{"missing"}, Name: "api"},
		"api.tar",
		archivePath,
	); err == nil {
		t.Fatal("internal archive import accepted a missing group")
	}
}

func TestStageRepositoryArchiveFailures(t *testing.T) {
	root := t.TempDir()
	store := Store{Root: root}
	missing := filepath.Join(root, "missing.zip")

	for _, test := range []struct {
		name     string
		filename string
		path     string
		prepare  func(string)
	}{
		{
			name:     "unsupported format",
			filename: "repository.rar",
			path:     missing,
		},
		{
			name:     "missing archive",
			filename: "repository.zip",
			path:     missing,
		},
		{
			name:     "corrupt ZIP",
			filename: "repository.zip",
			path:     filepath.Join(root, "corrupt.zip"),
			prepare: func(path string) {
				if err := os.WriteFile(path, []byte("not a zip"), 0o640); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:     "corrupt gzip",
			filename: "repository.tgz",
			path:     filepath.Join(root, "corrupt.tgz"),
			prepare: func(path string) {
				if err := os.WriteFile(path, []byte("not gzip"), 0o640); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:     "oversized archive",
			filename: "repository.zip",
			path:     filepath.Join(root, "oversized.zip"),
			prepare: func(path string) {
				if err := os.WriteFile(path, nil, 0o640); err != nil {
					t.Fatal(err)
				}
				if err := os.Truncate(path, MaximumImportArchiveBytes+1); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:     "archive without repository",
			filename: "repository.zip",
			path:     filepath.Join(root, "empty.zip"),
			prepare: func(path string) {
				writeTestZIP(t, path, []testZIPEntry{{name: "README", contents: "not bare"}})
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.prepare != nil {
				test.prepare(test.path)
			}
			temporary, repository, err := store.stageRepositoryArchive(
				context.Background(),
				test.filename,
				test.path,
			)
			if err == nil {
				_ = os.RemoveAll(temporary)
				t.Fatalf("stage unexpectedly succeeded with repository %q", repository)
			}
			if temporary != "" || repository != "" {
				t.Fatalf("failed stage returned paths %q, %q", temporary, repository)
			}
		})
	}

	blockedRoot := filepath.Join(t.TempDir(), "root-file")
	if err := os.WriteFile(blockedRoot, []byte("blocked"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := (Store{Root: blockedRoot}).newImportStagingDirectory(); err == nil {
		t.Fatal("staging directory was created below a regular file")
	}
}

func TestArchiveExtractionDefensiveBranches(t *testing.T) {
	root := t.TempDir()

	t.Run("unsupported dispatch", func(t *testing.T) {
		if err := extractRepositoryArchive(
			context.Background(),
			filepath.Join(root, "missing"),
			"repository.rar",
			t.TempDir(),
		); err == nil {
			t.Fatal("unsupported archive format was accepted")
		}
	})

	t.Run("ZIP root file", func(t *testing.T) {
		archivePath := filepath.Join(root, "root-file.zip")
		writeTestZIP(t, archivePath, []testZIPEntry{{name: ".", contents: "invalid"}})
		if err := extractZIPArchive(context.Background(), archivePath, t.TempDir()); err == nil {
			t.Fatal("ZIP root file was accepted")
		}
	})

	t.Run("ZIP special file", func(t *testing.T) {
		archivePath := filepath.Join(root, "special.zip")
		writeTestZIP(t, archivePath, []testZIPEntry{{
			name:     "link",
			contents: "target",
			mode:     os.ModeSymlink | 0o777,
		}})
		if err := extractZIPArchive(context.Background(), archivePath, t.TempDir()); err == nil {
			t.Fatal("ZIP symbolic link was accepted")
		}
	})

	t.Run("ZIP duplicate file", func(t *testing.T) {
		archivePath := filepath.Join(root, "duplicate.zip")
		writeTestZIP(t, archivePath, []testZIPEntry{
			{name: "duplicate", contents: "one"},
			{name: "duplicate", contents: "two"},
		})
		if err := extractZIPArchive(context.Background(), archivePath, t.TempDir()); err == nil {
			t.Fatal("duplicate ZIP file was accepted")
		}
	})

	t.Run("canceled ZIP", func(t *testing.T) {
		archivePath := filepath.Join(root, "canceled.zip")
		writeTestZIP(t, archivePath, []testZIPEntry{{name: "file", contents: "data"}})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := extractZIPArchive(ctx, archivePath, t.TempDir()); !errors.Is(
			err,
			context.Canceled,
		) {
			t.Fatalf("canceled ZIP error = %v", err)
		}
	})

	t.Run("invalid and canceled TAR", func(t *testing.T) {
		invalidPath := filepath.Join(root, "invalid.tar")
		if err := os.WriteFile(invalidPath, []byte("not a tar"), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := extractTARArchive(
			context.Background(),
			invalidPath,
			t.TempDir(),
			false,
		); err == nil {
			t.Fatal("invalid TAR was accepted")
		}

		archivePath := filepath.Join(root, "canceled.tar")
		writeTestTAR(t, archivePath, []testTAREntry{{name: "file", contents: "data"}})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := extractTARArchive(ctx, archivePath, t.TempDir(), false); !errors.Is(
			err,
			context.Canceled,
		) {
			t.Fatalf("canceled TAR error = %v", err)
		}
	})

	t.Run("TAR root file", func(t *testing.T) {
		archivePath := filepath.Join(root, "root-file.tar")
		writeTestTAR(t, archivePath, []testTAREntry{{name: ".", contents: "invalid"}})
		if err := extractTARArchive(
			context.Background(),
			archivePath,
			t.TempDir(),
			false,
		); err == nil {
			t.Fatal("TAR root file was accepted")
		}
	})
}

func TestArchivePathAndReaderValidation(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"", "bad\x00name", `bad\name`, "/absolute", "../escape"} {
		if _, _, err := archiveEntryPath(root, name); err == nil {
			t.Fatalf("archive entry %q was accepted", name)
		}
	}
	target, clean, err := archiveEntryPath(root, "folder/../file")
	if err != nil {
		t.Fatal(err)
	}
	if clean != "file" || target != filepath.Join(root, "file") {
		t.Fatalf("clean path = %q, %q", target, clean)
	}

	ctx, cancel := context.WithCancel(context.Background())
	reader := contextReader{ctx: ctx, reader: strings.NewReader("data")}
	buffer := make([]byte, 4)
	if count, readErr := reader.Read(buffer); readErr != nil || count != 4 {
		t.Fatalf("context reader read = %d, %v", count, readErr)
	}
	cancel()
	if _, readErr := reader.Read(buffer); !errors.Is(readErr, context.Canceled) {
		t.Fatalf("canceled context reader error = %v", readErr)
	}

	destination := filepath.Join(root, "file")
	if err = os.WriteFile(destination, []byte("existing"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err = extractRegularFile(
		context.Background(),
		strings.NewReader("new"),
		destination,
		10,
	); err == nil {
		t.Fatal("archive extraction replaced an existing file")
	}
	oversized := filepath.Join(root, "oversized")
	if _, err = extractRegularFile(
		context.Background(),
		strings.NewReader("too large"),
		oversized,
		3,
	); err == nil {
		t.Fatal("oversized extracted file was accepted")
	}
	canceled := filepath.Join(root, "canceled")
	if _, err = extractRegularFile(ctx, strings.NewReader("data"), canceled, 10); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("canceled extraction error = %v", err)
	}
}

func TestFindAndValidateArchivedRepositories(t *testing.T) {
	empty := t.TempDir()
	if _, err := findArchivedBareRepository(empty); err == nil {
		t.Fatal("empty archive root was accepted")
	}

	multiple := t.TempDir()
	for _, name := range []string{"first.git", "second.git"} {
		if _, err := initBareRepository(filepath.Join(multiple, name)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := findArchivedBareRepository(multiple); err == nil {
		t.Fatal("multiple bare repositories were accepted")
	}

	invalid := t.TempDir()
	if isBareRepository(invalid) {
		t.Fatal("empty directory was identified as a bare repository")
	}
	if err := os.Mkdir(filepath.Join(invalid, "HEAD"), 0o750); err != nil {
		t.Fatal(err)
	}
	if isBareRepository(invalid) {
		t.Fatal("repository with directory HEAD was accepted")
	}
	if err := os.RemoveAll(filepath.Join(invalid, "HEAD")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(invalid, "HEAD"), []byte("ref: refs/heads/main\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(invalid, "objects"), []byte("not a directory"), 0o640); err != nil {
		t.Fatal(err)
	}
	if isBareRepository(invalid) {
		t.Fatal("repository with file objects store was accepted")
	}
	if err := os.Remove(filepath.Join(invalid, "objects")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(invalid, "objects"), 0o750); err != nil {
		t.Fatal(err)
	}
	if isBareRepository(invalid) {
		t.Fatal("non-Git directory structure was identified as a bare repository")
	}
}

func TestPrepareAndAdoptImportFailures(t *testing.T) {
	store := Store{Root: t.TempDir()}
	if _, err := store.prepareImportDestination(repopath.Repository{
		Groups: []string{".."},
		Name:   "api",
	}); err == nil {
		t.Fatal("invalid import group was accepted")
	}
	if _, err := store.prepareImportDestination(repopath.Repository{
		Groups: []string{"missing"},
		Name:   "api",
	}); err == nil {
		t.Fatal("missing import group was accepted")
	}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	target := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	buildPath, err := store.BuildPath(target)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(buildPath, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err = store.prepareImportDestination(target); err == nil {
		t.Fatal("orphaned build data was accepted as an import destination")
	}

	root := t.TempDir()
	if err = adoptImportedRepository(
		filepath.Join(root, "missing"),
		filepath.Join(root, "repository.git"),
		filepath.Join(root, "repository.lfs"),
	); err == nil {
		t.Fatal("missing staged repository was adopted")
	}

	source := filepath.Join(root, "source.git")
	if _, err = initBareRepository(source); err != nil {
		t.Fatal(err)
	}
	blockedDestination := filepath.Join(root, "missing-parent", "repository.git")
	if err = adoptImportedRepository(
		source,
		blockedDestination,
		filepath.Join(root, "repository.lfs"),
	); err == nil {
		t.Fatal("repository was adopted below a missing parent")
	}

	source = filepath.Join(root, "second-source.git")
	if _, err = initBareRepository(source); err != nil {
		t.Fatal(err)
	}
	gitPath := filepath.Join(root, "adopted.git")
	lfsPath := filepath.Join(root, "adopted.lfs")
	if err = os.WriteFile(lfsPath, []byte("blocked"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err = adoptImportedRepository(source, gitPath, lfsPath); err == nil {
		t.Fatal("repository adoption ignored an invalid LFS destination")
	}
	if _, err = os.Stat(gitPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed adoption left Git data: %v", err)
	}
}

type testZIPEntry struct {
	name     string
	contents string
	mode     os.FileMode
}

func writeTestZIP(t *testing.T, archivePath string, entries []testZIPEntry) {
	t.Helper()
	output, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(output)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Store}
		mode := entry.mode
		if mode == 0 {
			mode = 0o640
		}
		header.SetMode(mode)
		target, createErr := writer.CreateHeader(header)
		if createErr != nil {
			t.Fatal(createErr)
		}
		if _, createErr = io.WriteString(target, entry.contents); createErr != nil {
			t.Fatal(createErr)
		}
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err = output.Close(); err != nil {
		t.Fatal(err)
	}
}

type testTAREntry struct {
	name     string
	contents string
}

func writeTestTAR(t *testing.T, archivePath string, entries []testTAREntry) {
	t.Helper()
	output, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(output)
	for _, entry := range entries {
		if err = writer.WriteHeader(&tar.Header{
			Name:     entry.name,
			Mode:     0o640,
			Size:     int64(len(entry.contents)),
			Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err = io.Copy(writer, bytes.NewBufferString(entry.contents)); err != nil {
			t.Fatal(err)
		}
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err = output.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeRepositoryTAR(
	t *testing.T,
	repositoryPath string,
	archivePath string,
	prefix string,
) {
	t.Helper()
	output, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(output)
	err = filepath.Walk(repositoryPath, func(current string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, relativeErr := filepath.Rel(repositoryPath, current)
		if relativeErr != nil {
			return relativeErr
		}
		if relative == "." {
			return nil
		}
		header, headerErr := tar.FileInfoHeader(info, "")
		if headerErr != nil {
			return headerErr
		}
		header.Name = filepath.ToSlash(filepath.Join(prefix, relative))
		if headerErr = writer.WriteHeader(header); headerErr != nil || info.IsDir() {
			return headerErr
		}
		source, openErr := os.Open(current)
		if openErr != nil {
			return openErr
		}
		_, copyErr := io.Copy(writer, source)
		closeErr := source.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err = output.Close(); err != nil {
		t.Fatal(err)
	}
}

func initBareRepository(path string) (*git.Repository, error) {
	return gitformat.Init(path, true)
}
