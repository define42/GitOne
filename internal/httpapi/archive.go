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
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

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

	return &huma.StreamResponse{
		Body: func(response huma.Context) {
			response.SetHeader("Content-Type", contentType)
			response.SetHeader(
				"Content-Disposition",
				mime.FormatMediaType("attachment", map[string]string{"filename": fileName}),
			)
			response.SetHeader("Cache-Control", "no-store")
			response.SetHeader("X-Content-Type-Options", "nosniff")
			var writeErr error
			if format == "zip" {
				writeErr = writeRepositoryZIP(tree, commit, prefix, response.BodyWriter())
			} else {
				writeErr = writeRepositoryTarGzip(tree, commit, prefix, response.BodyWriter())
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
		writer, err := archive.CreateHeader(header)
		if err != nil {
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
		reader, err := file.Reader()
		if err != nil {
			return err
		}
		_, writeErr := io.Copy(writer, reader)
		closeErr := reader.Close()
		return errors.Join(writeErr, closeErr)
	})
	return errors.Join(err, archive.Close())
}

func writeRepositoryTarGzip(
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
		if err = archive.WriteHeader(header); err != nil {
			return err
		}
		if header.Typeflag == tar.TypeSymlink {
			return nil
		}
		reader, err := file.Reader()
		if err != nil {
			return err
		}
		_, writeErr := io.Copy(archive, reader)
		closeErr := reader.Close()
		return errors.Join(writeErr, closeErr)
	})
	return errors.Join(err, archive.Close(), compressed.Close())
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
