package storage

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/define42/GitOne/internal/lockmgr"
	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/review"
	git "github.com/go-git/go-git/v5"
)

const (
	// MaximumImportArchiveBytes is the largest compressed repository archive
	// accepted by the HTTP upload endpoint.
	MaximumImportArchiveBytes int64 = 1 << 30

	maximumImportExtractedBytes int64 = 4 << 30
	maximumImportArchiveEntries       = 100_000
)

type ArchiveImportError struct {
	Err error
}

func (e *ArchiveImportError) Error() string {
	return "invalid repository archive: " + e.Err.Error()
}

func (e *ArchiveImportError) Unwrap() error {
	return e.Err
}

type importDestination struct {
	gitPath string
	lfsPath string
}

// IsSupportedImportArchive reports whether a filename has one of the archive
// extensions accepted for repository imports.
func IsSupportedImportArchive(filename string) bool {
	lower := strings.ToLower(strings.TrimSpace(filename))
	return strings.HasSuffix(lower, ".zip") ||
		strings.HasSuffix(lower, ".tar") ||
		strings.HasSuffix(lower, ".tar.gz") ||
		strings.HasSuffix(lower, ".tgz")
}

// ImportRepositoryArchive extracts a ZIP or TAR archive containing a bare Git
// repository and adopts it as a new repository.
func (s Store) ImportRepositoryArchive(
	ctx context.Context,
	r repopath.Repository,
	filename string,
	archivePath string,
) error {
	return s.ImportRepositoryArchiveValidated(ctx, r, filename, archivePath, nil)
}

// ImportRepositoryArchiveValidated extracts and validates the archive without
// holding a repository lock. It locks only for final validation and publish.
func (s Store) ImportRepositoryArchiveValidated(
	ctx context.Context,
	r repopath.Repository,
	filename string,
	archivePath string,
	validate func() error,
) error {
	if r.Name == "control" {
		return errors.New("reserved repository name")
	}
	if err := s.checkImportDestination(r); err != nil {
		return err
	}
	temporaryPath, repositoryPath, err := s.stageRepositoryArchive(
		ctx,
		filename,
		archivePath,
	)
	if err != nil {
		return err
	}
	defer func() {
		_ = os.RemoveAll(temporaryPath)
	}()

	releaseOperation, err := lockmgr.Process.Acquire(
		lockmgr.RepositoryRequests(s.Root, []repopath.Repository{r}, lockmgr.Exclusive)...,
	)
	if err != nil {
		return err
	}
	defer releaseOperation()
	if validate != nil {
		if err = validate(); err != nil {
			return err
		}
	}
	return review.NewStore(s.Root).WithRepositoryLocks([]repopath.Repository{r}, func() error {
		destination, prepareErr := s.prepareImportDestination(r)
		if prepareErr != nil {
			return prepareErr
		}
		return adoptImportedRepository(
			repositoryPath,
			destination.gitPath,
			destination.lfsPath,
		)
	})
}

// ImportRepositoryArchiveLocked imports an archive while its caller holds the
// repository operations lock.
func (s Store) ImportRepositoryArchiveLocked(
	ctx context.Context,
	r repopath.Repository,
	filename string,
	archivePath string,
) error {
	if r.Name == "control" {
		return errors.New("reserved repository name")
	}
	return review.NewStore(s.Root).WithRepositoryLocks([]repopath.Repository{r}, func() error {
		return s.importRepositoryArchive(ctx, r, filename, archivePath)
	})
}

func (s Store) importRepositoryArchive(
	ctx context.Context,
	r repopath.Repository,
	filename string,
	archivePath string,
) error {
	destination, err := s.prepareImportDestination(r)
	if err != nil {
		return err
	}
	temporaryPath, repositoryPath, err := s.stageRepositoryArchive(
		ctx,
		filename,
		archivePath,
	)
	if err != nil {
		return err
	}
	defer func() {
		_ = os.RemoveAll(temporaryPath)
	}()
	return adoptImportedRepository(
		repositoryPath,
		destination.gitPath,
		destination.lfsPath,
	)
}

func (s Store) stageRepositoryArchive(
	ctx context.Context,
	filename string,
	archivePath string,
) (string, string, error) {
	if !IsSupportedImportArchive(filename) {
		return "", "", &ArchiveImportError{
			Err: errors.New("supported formats are .zip, .tar, .tar.gz, and .tgz"),
		}
	}
	archiveInfo, err := os.Stat(archivePath)
	if err != nil {
		return "", "", err
	}
	if archiveInfo.Size() > MaximumImportArchiveBytes {
		return "", "", &ArchiveImportError{Err: errors.New("archive exceeds the 1 GiB limit")}
	}

	temporaryPath, err := s.newImportStagingDirectory()
	if err != nil {
		return "", "", err
	}

	if err = extractRepositoryArchive(ctx, archivePath, filename, temporaryPath); err != nil {
		_ = os.RemoveAll(temporaryPath)
		return "", "", &ArchiveImportError{Err: err}
	}
	repositoryPath, err := findArchivedBareRepository(temporaryPath)
	if err != nil {
		_ = os.RemoveAll(temporaryPath)
		return "", "", &ArchiveImportError{Err: err}
	}
	if err = sanitizeArchivedRepository(repositoryPath); err != nil {
		_ = os.RemoveAll(temporaryPath)
		return "", "", &ArchiveImportError{Err: err}
	}
	if !isBareRepository(repositoryPath) {
		_ = os.RemoveAll(temporaryPath)
		return "", "", &ArchiveImportError{
			Err: errors.New("archive does not contain a valid bare Git repository"),
		}
	}
	return temporaryPath, repositoryPath, nil
}

func (s Store) prepareImportDestination(r repopath.Repository) (importDestination, error) {
	exists, err := s.groupExists(r.Group())
	if err != nil {
		return importDestination{}, err
	}
	if !exists {
		return importDestination{}, errors.New("group does not exist")
	}
	gitPath, err := s.GitPath(r)
	if err != nil {
		return importDestination{}, err
	}
	lfsPath, err := s.LFSPath(r)
	if err != nil {
		return importDestination{}, err
	}
	buildPath, err := s.BuildPath(r)
	if err != nil {
		return importDestination{}, err
	}
	reviewPath, err := s.ReviewPath(r)
	if err != nil {
		return importDestination{}, err
	}
	for _, existing := range []string{gitPath, lfsPath, buildPath, reviewPath} {
		exists, statErr := pathEntryExists(existing)
		if statErr != nil {
			return importDestination{}, statErr
		}
		if exists {
			return importDestination{}, errors.New("repository data already exists")
		}
	}
	return importDestination{
		gitPath: gitPath,
		lfsPath: lfsPath,
	}, nil
}

func (s Store) newImportStagingDirectory() (string, error) {
	root, err := repopath.SafeJoin(s.Root, ".gitone", "imports")
	if err != nil {
		return "", err
	}
	if err = os.MkdirAll(root, 0o750); err != nil {
		return "", err
	}
	return os.MkdirTemp(root, "repository-*")
}

func adoptImportedRepository(sourcePath, gitPath, lfsPath string) error {
	if err := os.Chmod(sourcePath, 0o750); err != nil {
		return err
	}
	if err := os.Rename(sourcePath, gitPath); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(lfsPath, "objects"), 0o750); err != nil {
		_ = os.RemoveAll(gitPath)
		_ = os.RemoveAll(lfsPath)
		return err
	}
	return nil
}

func extractRepositoryArchive(
	ctx context.Context,
	archivePath string,
	filename string,
	destination string,
) error {
	lower := strings.ToLower(strings.TrimSpace(filename))
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return extractZIPArchive(ctx, archivePath, destination)
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return extractTARArchive(ctx, archivePath, destination, true)
	case strings.HasSuffix(lower, ".tar"):
		return extractTARArchive(ctx, archivePath, destination, false)
	default:
		return errors.New("unsupported archive format")
	}
}

func extractZIPArchive(ctx context.Context, archivePath, destination string) error {
	archive, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer func() {
		_ = archive.Close()
	}()
	info, err := archive.Stat()
	if err != nil {
		return err
	}
	reader, err := zip.NewReader(archive, info.Size())
	if err != nil {
		return errors.New("could not read ZIP archive")
	}
	if len(reader.File) > maximumImportArchiveEntries {
		return errors.New("archive contains too many entries")
	}

	var extractedBytes int64
	for _, entry := range reader.File {
		if err = ctx.Err(); err != nil {
			return err
		}
		target, cleanName, pathErr := archiveEntryPath(destination, entry.Name)
		if pathErr != nil {
			return pathErr
		}
		if cleanName == "." {
			if entry.FileInfo().IsDir() {
				continue
			}
			return errors.New("archive contains an invalid root file entry")
		}
		mode := entry.Mode()
		if entry.FileInfo().IsDir() {
			if err = os.MkdirAll(target, 0o750); err != nil {
				return err
			}
			continue
		}
		if !mode.IsRegular() {
			return fmt.Errorf("archive entry %q is not a regular file", entry.Name)
		}
		if entry.UncompressedSize64 > uint64(maximumImportExtractedBytes) ||
			extractedBytes > maximumImportExtractedBytes-int64(entry.UncompressedSize64) {
			return errors.New("archive expands beyond the allowed size")
		}
		if err = os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return err
		}
		source, openErr := entry.Open()
		if openErr != nil {
			return errors.New("could not read ZIP archive entry")
		}
		written, copyErr := extractRegularFile(
			ctx,
			source,
			target,
			maximumImportExtractedBytes-extractedBytes,
		)
		closeErr := source.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		extractedBytes += written
	}
	return nil
}

func extractTARArchive(
	ctx context.Context,
	archivePath string,
	destination string,
	compressed bool,
) error {
	archive, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer func() {
		_ = archive.Close()
	}()

	var source io.Reader = archive
	var compressedReader *gzip.Reader
	if compressed {
		compressedReader, err = gzip.NewReader(archive)
		if err != nil {
			return errors.New("could not read gzip-compressed TAR archive")
		}
		defer func() {
			_ = compressedReader.Close()
		}()
		source = compressedReader
	}

	reader := tar.NewReader(source)
	var extractedBytes int64
	entryCount := 0
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		header, nextErr := reader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return errors.New("could not read TAR archive")
		}
		entryCount++
		if entryCount > maximumImportArchiveEntries {
			return errors.New("archive contains too many entries")
		}
		target, cleanName, pathErr := archiveEntryPath(destination, header.Name)
		if pathErr != nil {
			return pathErr
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if cleanName == "." {
				continue
			}
			if err = os.MkdirAll(target, 0o750); err != nil {
				return err
			}
		case tar.TypeReg:
			if cleanName == "." {
				return errors.New("archive contains an invalid root file entry")
			}
			if header.Size < 0 ||
				header.Size > maximumImportExtractedBytes ||
				extractedBytes > maximumImportExtractedBytes-header.Size {
				return errors.New("archive expands beyond the allowed size")
			}
			if err = os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return err
			}
			written, copyErr := extractRegularFile(
				ctx,
				io.LimitReader(reader, header.Size),
				target,
				maximumImportExtractedBytes-extractedBytes,
			)
			if copyErr != nil {
				return copyErr
			}
			if written != header.Size {
				return errors.New("archive contains a truncated file")
			}
			extractedBytes += written
		default:
			return fmt.Errorf(
				"archive entry %q has a disallowed link or special-file type",
				header.Name,
			)
		}
	}
	return nil
}

func archiveEntryPath(root, name string) (string, string, error) {
	if name == "" || strings.ContainsRune(name, '\x00') || strings.Contains(name, "\\") {
		return "", "", errors.New("archive contains an invalid entry path")
	}
	cleanName := path.Clean(name)
	if path.IsAbs(cleanName) ||
		cleanName == ".." ||
		strings.HasPrefix(cleanName, "../") {
		return "", "", fmt.Errorf("archive entry %q escapes the repository", name)
	}
	target := filepath.Join(root, filepath.FromSlash(cleanName))
	relative, err := filepath.Rel(root, target)
	if err != nil ||
		relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("archive entry %q escapes the repository", name)
	}
	return target, cleanName, nil
}

func extractRegularFile(
	ctx context.Context,
	source io.Reader,
	target string,
	maximumBytes int64,
) (int64, error) {
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return 0, err
	}
	written, copyErr := io.Copy(
		output,
		io.LimitReader(contextReader{ctx: ctx, reader: source}, maximumBytes+1),
	)
	closeErr := output.Close()
	if copyErr != nil {
		return written, copyErr
	}
	if closeErr != nil {
		return written, closeErr
	}
	if written > maximumBytes {
		return written, errors.New("archive expands beyond the allowed size")
	}
	return written, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

func findArchivedBareRepository(root string) (string, error) {
	if isBareRepository(root) {
		return root, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	candidates := make([]string, 0, 1)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		candidate := filepath.Join(root, entry.Name())
		if isBareRepository(candidate) {
			candidates = append(candidates, candidate)
		}
	}
	switch len(candidates) {
	case 1:
		return candidates[0], nil
	case 0:
		return "", errors.New(
			"archive must contain a bare Git repository at its root or in one enclosing folder",
		)
	default:
		return "", errors.New("archive contains multiple bare Git repositories")
	}
}

func isBareRepository(repositoryPath string) bool {
	head, err := os.Stat(filepath.Join(repositoryPath, "HEAD"))
	if err != nil || !head.Mode().IsRegular() {
		return false
	}
	objects, err := os.Stat(filepath.Join(repositoryPath, "objects"))
	if err != nil || !objects.IsDir() {
		return false
	}
	repository, err := git.PlainOpen(repositoryPath)
	if err != nil {
		return false
	}
	configuration, err := repository.Config()
	if err != nil || !configuration.Core.IsBare {
		return false
	}
	_, err = repository.Worktree()
	return errors.Is(err, git.ErrIsBareRepository)
}

func sanitizeArchivedRepository(repositoryPath string) error {
	if err := os.RemoveAll(filepath.Join(repositoryPath, "hooks")); err != nil {
		return err
	}
	for _, alternates := range []string{
		filepath.Join(repositoryPath, "objects", "info", "alternates"),
		filepath.Join(repositoryPath, "objects", "info", "http-alternates"),
	} {
		if err := os.Remove(alternates); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
