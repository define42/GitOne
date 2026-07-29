package runner

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/storage"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func WriteSourceArchive(
	store storage.Store,
	repositoryPath repopath.Repository,
	commitHash plumbing.Hash,
	output io.Writer,
) error {
	gitPath, err := store.GitPath(repositoryPath)
	if err != nil {
		return err
	}
	repository, err := git.PlainOpen(gitPath)
	if err != nil {
		return err
	}
	commit, err := repository.CommitObject(commitHash)
	if err != nil {
		return err
	}
	tree, err := commit.Tree()
	if err != nil {
		return err
	}
	compressed := gzip.NewWriter(output)
	archive := tar.NewWriter(compressed)
	files := tree.Files()
	err = files.ForEach(func(file *object.File) error {
		name := path.Clean(file.Name)
		if name != file.Name || name == "." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("unsafe repository path %q", file.Name)
		}
		header := &tar.Header{
			Name:    name,
			Mode:    0o644,
			Size:    file.Size,
			ModTime: commit.Committer.When,
		}
		switch file.Mode {
		case filemode.Executable:
			header.Mode = 0o755
		case filemode.Symlink:
			contents, readErr := file.Contents()
			if readErr != nil {
				return readErr
			}
			header.Typeflag = tar.TypeSymlink
			header.Linkname = contents
			header.Size = 0
		case filemode.Regular, filemode.Deprecated:
		default:
			return nil
		}
		if writeErr := archive.WriteHeader(header); writeErr != nil {
			return writeErr
		}
		if header.Typeflag != tar.TypeSymlink {
			reader, readErr := file.Reader()
			if readErr != nil {
				return readErr
			}
			_, writeErr := io.Copy(archive, reader)
			closeErr := reader.Close()
			return errors.Join(writeErr, closeErr)
		}
		return nil
	})
	closeErr := archive.Close()
	compressErr := compressed.Close()
	return errors.Join(err, closeErr, compressErr)
}

func ExtractSourceArchive(input io.Reader, destination string) error {
	compressed, err := gzip.NewReader(input)
	if err != nil {
		return err
	}
	defer func() {
		_ = compressed.Close()
	}()
	archive := tar.NewReader(compressed)
	for {
		header, readErr := archive.Next()
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
		name := path.Clean(header.Name)
		if name != header.Name || name == "." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("unsafe source archive path %q", header.Name)
		}
		target, joinErr := repopath.SafeJoin(
			destination,
			strings.Split(filepath.FromSlash(name), string(filepath.Separator))...,
		)
		if joinErr != nil {
			return joinErr
		}
		switch header.Typeflag {
		case tar.TypeReg:
			if err = os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return err
			}
			file, openErr := os.OpenFile(
				target,
				os.O_CREATE|os.O_WRONLY|os.O_TRUNC,
				os.FileMode(header.Mode)&0o777,
			)
			if openErr != nil {
				return openErr
			}
			_, copyErr := io.Copy(file, archive)
			closeErr := file.Close()
			if copyErr != nil || closeErr != nil {
				return errors.Join(copyErr, closeErr)
			}
		case tar.TypeSymlink:
			if err = os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return err
			}
			if filepath.IsAbs(header.Linkname) {
				return fmt.Errorf("unsafe absolute source symlink %q", header.Name)
			}
			resolved := filepath.Clean(filepath.Join(filepath.Dir(target), header.Linkname))
			if _, joinErr = repopath.SafeJoin(destination, relativeParts(destination, resolved)...); joinErr != nil {
				return fmt.Errorf("unsafe source symlink %q: %w", header.Name, joinErr)
			}
			if err = os.Symlink(header.Linkname, target); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported source archive entry %q", header.Name)
		}
	}
}

func relativeParts(root, target string) []string {
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return []string{".."}
	}
	return strings.Split(relative, string(filepath.Separator))
}
