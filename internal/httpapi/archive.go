package httpapi

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"os"
	"path"
	"strings"
	"unicode"

	"github.com/danielgtaylor/huma/v2"
	"github.com/define42/GitOne/internal/httpio"
	"github.com/define42/GitOne/internal/lfs"
	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/storage"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
)

type repositoryArchiveSource struct {
	storage    storage.Store
	repository repopath.Repository
}

func (a API) downloadRepositoryArchive(
	ctx context.Context,
	input *repositoryArchiveInput,
) (*huma.StreamResponse, error) {
	repository, parsed, err := a.openBrowsableRepository(
		ctx,
		input.AuthInput,
		input.Repository,
	)
	if err != nil {
		return nil, err
	}
	commit, err := resolveBrowserCommit(repository, input.Ref)
	if err != nil {
		return nil, huma.Error404NotFound("Git reference not found", err)
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, huma.Error500InternalServerError("could not load Git tree", err)
	}
	format := input.Format
	if format == "" {
		format = "zip"
	}
	contentType := ""
	switch format {
	case "zip":
		contentType = "application/zip"
	case "tar.gz":
		contentType = "application/gzip"
	default:
		return nil, huma.Error400BadRequest("archive format must be zip or tar.gz")
	}
	baseName := archiveName(parsed.Name, input.Ref)
	fileName := baseName + "." + format
	prefix := baseName + "/"
	source := repositoryArchiveSource{
		storage:    a.Storage,
		repository: parsed,
	}
	if err = source.verifyLFSObjects(tree); err != nil {
		return nil, huma.Error500InternalServerError(
			"could not resolve repository LFS objects",
			err,
		)
	}

	return &huma.StreamResponse{
		Body: func(response huma.Context) {
			response.SetHeader("Content-Type", contentType)
			response.SetHeader(
				"Content-Disposition",
				mime.FormatMediaType("attachment", map[string]string{"filename": fileName}),
			)
			response.SetHeader("Cache-Control", "no-store")
			response.SetHeader("X-Content-Type-Options", "nosniff")
			output, cleanup := httpio.ProtectWriter(
				response.BodyWriter(),
				httpio.DefaultIdleTimeout,
			)
			defer cleanup()
			var writeErr error
			if format == "zip" {
				writeErr = writeRepositoryZIP(source, tree, commit, prefix, output)
			} else {
				writeErr = writeRepositoryTarGzip(source, tree, commit, prefix, output)
			}
			if writeErr != nil {
				log.Printf(
					"could not stream %s archive for %s@%s: %v",
					format,
					parsed.Full(),
					commit.Hash,
					writeErr,
				)
			}
		},
	}, nil
}

func writeRepositoryZIP(
	source repositoryArchiveSource,
	tree *object.Tree,
	commit *object.Commit,
	prefix string,
	output io.Writer,
) error {
	archive := zip.NewWriter(output)
	files := tree.Files()
	err := files.ForEach(func(file *object.File) error {
		name, err := archiveEntryName(prefix, file.Name)
		if err != nil {
			return err
		}
		header := &zip.FileHeader{
			Name:     name,
			Method:   zip.Deflate,
			Modified: commit.Committer.When,
		}
		switch file.Mode {
		case filemode.Executable:
			header.SetMode(0o755)
		case filemode.Symlink:
			header.SetMode(os.ModeSymlink | 0o777)
		case filemode.Regular, filemode.Deprecated:
			header.SetMode(0o644)
		default:
			return nil
		}
		var reader io.ReadCloser
		if file.Mode != filemode.Symlink {
			reader, _, err = source.open(file)
			if err != nil {
				return err
			}
		}
		writer, err := archive.CreateHeader(header)
		if err != nil {
			if reader != nil {
				return errors.Join(err, reader.Close())
			}
			return err
		}
		if file.Mode == filemode.Symlink {
			contents, readErr := file.Contents()
			if readErr != nil {
				return readErr
			}
			_, err = io.WriteString(writer, contents)
			return err
		}
		_, writeErr := io.Copy(writer, reader)
		closeErr := reader.Close()
		return errors.Join(writeErr, closeErr)
	})
	return errors.Join(err, archive.Close())
}

func writeRepositoryTarGzip(
	source repositoryArchiveSource,
	tree *object.Tree,
	commit *object.Commit,
	prefix string,
	output io.Writer,
) error {
	compressed := gzip.NewWriter(output)
	archive := tar.NewWriter(compressed)
	files := tree.Files()
	err := files.ForEach(func(file *object.File) error {
		name, err := archiveEntryName(prefix, file.Name)
		if err != nil {
			return err
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
		var reader io.ReadCloser
		if header.Typeflag != tar.TypeSymlink {
			reader, header.Size, err = source.open(file)
			if err != nil {
				return err
			}
		}
		if err = archive.WriteHeader(header); err != nil {
			if reader != nil {
				return errors.Join(err, reader.Close())
			}
			return err
		}
		if header.Typeflag == tar.TypeSymlink {
			return nil
		}
		_, writeErr := io.Copy(archive, reader)
		closeErr := reader.Close()
		return errors.Join(writeErr, closeErr)
	})
	return errors.Join(err, archive.Close(), compressed.Close())
}

func (s repositoryArchiveSource) verifyLFSObjects(tree *object.Tree) error {
	return tree.Files().ForEach(func(file *object.File) error {
		switch file.Mode {
		case filemode.Executable, filemode.Regular, filemode.Deprecated:
		default:
			return nil
		}
		pointer, ok, err := repositoryArchiveLFSPointer(file)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if err = lfs.VerifyObject(s.storage, s.repository, pointer.OID, pointer.Size); err != nil {
			return fmt.Errorf("resolve LFS object for %q: %w", file.Name, err)
		}
		return nil
	})
}

func (s repositoryArchiveSource) open(file *object.File) (io.ReadCloser, int64, error) {
	pointer, ok, err := repositoryArchiveLFSPointer(file)
	if err != nil {
		return nil, 0, err
	}
	if !ok {
		reader, openErr := file.Reader()
		return reader, file.Size, openErr
	}

	reader, err := lfs.OpenObject(s.storage, s.repository, pointer.OID)
	if err != nil {
		return nil, 0, fmt.Errorf("open LFS object for %q: %w", file.Name, err)
	}
	info, err := reader.Stat()
	if err != nil {
		return nil, 0, errors.Join(
			fmt.Errorf("inspect LFS object for %q: %w", file.Name, err),
			reader.Close(),
		)
	}
	if !info.Mode().IsRegular() {
		return nil, 0, errors.Join(
			fmt.Errorf("LFS object for %q is not a regular file", file.Name),
			reader.Close(),
		)
	}
	if info.Size() != pointer.Size {
		return nil, 0, errors.Join(
			fmt.Errorf(
				"LFS object for %q has size %d, expected %d",
				file.Name,
				info.Size(),
				pointer.Size,
			),
			reader.Close(),
		)
	}
	return reader, pointer.Size, nil
}

func repositoryArchiveLFSPointer(file *object.File) (lfs.Pointer, bool, error) {
	if file.Size > lfs.MaxPointerSize {
		return lfs.Pointer{}, false, nil
	}
	reader, err := file.Reader()
	if err != nil {
		return lfs.Pointer{}, false, fmt.Errorf("open Git blob %q: %w", file.Name, err)
	}
	content, readErr := io.ReadAll(io.LimitReader(reader, lfs.MaxPointerSize+1))
	closeErr := reader.Close()
	if err = errors.Join(readErr, closeErr); err != nil {
		return lfs.Pointer{}, false, fmt.Errorf("read Git blob %q: %w", file.Name, err)
	}
	pointer, ok := lfs.ParsePointer(content)
	return pointer, ok, nil
}

func archiveEntryName(prefix, fileName string) (string, error) {
	cleaned := path.Clean(fileName)
	if cleaned != fileName || cleaned == "." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("unsafe repository archive path %q", fileName)
	}
	return prefix + cleaned, nil
}

func archiveName(repository, ref string) string {
	value := repository + "-" + ref
	var name strings.Builder
	separator := false
	for _, character := range value {
		if unicode.IsLetter(character) ||
			unicode.IsDigit(character) ||
			character == '.' ||
			character == '_' ||
			character == '-' {
			name.WriteRune(character)
			separator = false
			continue
		}
		if name.Len() > 0 && !separator {
			name.WriteByte('-')
			separator = true
		}
	}
	result := strings.Trim(name.String(), "-.")
	if result == "" {
		return "repository"
	}
	return result
}
