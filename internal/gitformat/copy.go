package gitformat

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
)

// CopySHA256Repository validates source's complete reachable graph and copies
// only that graph and its refs into a fresh, bare SHA-256 repository. Repository
// metadata, reflogs, hooks, unreachable objects, and foreign object files are
// deliberately not carried across the import boundary.
func CopySHA256Repository(
	source *git.Repository,
	destinationPath string,
) (_ *git.Repository, retErr error) {
	objects, err := validatedReachableObjects(source)
	if err != nil {
		return nil, err
	}

	destinationPath, err = filepath.Abs(destinationPath)
	if err != nil {
		return nil, fmt.Errorf("resolve destination path: %w", err)
	}
	if _, err = os.Lstat(destinationPath); err == nil {
		return nil, fmt.Errorf("destination already exists: %s", destinationPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect destination: %w", err)
	}
	if err = os.Mkdir(destinationPath, 0o700); err != nil {
		return nil, fmt.Errorf("create destination: %w", err)
	}

	var target *git.Repository
	defer func() {
		if retErr == nil {
			return
		}
		if target != nil {
			_ = target.Close()
		}
		if err := os.RemoveAll(destinationPath); err != nil {
			retErr = errors.Join(
				retErr,
				fmt.Errorf("remove failed copy destination: %w", err),
			)
		}
	}()

	target, err = Init(destinationPath, true)
	if err != nil {
		return nil, fmt.Errorf("initialize SHA-256 destination: %w", err)
	}

	objectIDs := make([]string, 0, len(objects))
	for objectID := range objects {
		objectIDs = append(objectIDs, objectID)
	}
	sort.Strings(objectIDs)
	for _, objectID := range objectIDs {
		id, ok := plumbing.FromHex(objectID)
		if !ok {
			return nil, fmt.Errorf("validated object has invalid ID %q", objectID)
		}
		if err = copyEncodedObject(source, target, id, objects[objectID]); err != nil {
			return nil, fmt.Errorf("copy object %s: %w", id, err)
		}
	}

	references, err := source.References()
	if err != nil {
		return nil, fmt.Errorf("read source references: %w", err)
	}
	err = references.ForEach(func(reference *plumbing.Reference) error {
		var copied *plumbing.Reference
		switch reference.Type() {
		case plumbing.HashReference:
			copied = plumbing.NewHashReference(reference.Name(), reference.Hash())
		case plumbing.SymbolicReference:
			copied = plumbing.NewSymbolicReference(reference.Name(), reference.Target())
		default:
			return fmt.Errorf(
				"reference %s has unsupported type %s",
				reference.Name(),
				reference.Type(),
			)
		}
		if err := target.Storer.SetReference(copied); err != nil {
			return fmt.Errorf("write reference %s: %w", reference.Name(), err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err = ValidateReachable(target); err != nil {
		return nil, fmt.Errorf("validate copied repository: %w", err)
	}
	return target, nil
}

func copyEncodedObject(
	source *git.Repository,
	target *git.Repository,
	id plumbing.Hash,
	typ plumbing.ObjectType,
) error {
	object, err := source.Storer.EncodedObject(typ, id)
	if err != nil {
		return err
	}
	if object.Size() < 0 {
		return fmt.Errorf("object declares negative size %d", object.Size())
	}
	reader, err := object.Reader()
	if err != nil {
		return err
	}
	writer, err := target.Storer.RawObjectWriter(typ, object.Size())
	if err != nil {
		_ = reader.Close()
		return err
	}
	written, copyErr := io.Copy(writer, reader)
	readCloseErr := reader.Close()
	writeCloseErr := writer.Close()
	if err = errors.Join(copyErr, readCloseErr, writeCloseErr); err != nil {
		return err
	}
	if written != object.Size() {
		return fmt.Errorf(
			"object declares size %d but contains %d bytes",
			object.Size(),
			written,
		)
	}
	if err = target.Storer.HasEncodedObject(id); err != nil {
		return fmt.Errorf("copied object ID changed: %w", err)
	}
	return nil
}
