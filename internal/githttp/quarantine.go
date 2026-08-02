package githttp

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"

	"github.com/go-git/go-billy/v5/osfs"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/storer"
	gitstorage "github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/storage/transactional"
)

// receiveQuarantine keeps an incoming pack outside the live object directory.
// Its repository reads existing objects from the live storer and new objects
// from the temporary storer, allowing validation before publication.
type receiveQuarantine struct {
	Root       string
	Repository *git.Repository

	temporary *filesystem.Storage
}

type quarantineStorer struct {
	gitstorage.Storer
	objects    *transactional.ObjectStorage
	packWriter storer.PackfileWriter
}

func (s *quarantineStorer) NewEncodedObject() plumbing.EncodedObject {
	return s.objects.NewEncodedObject()
}

func (s *quarantineStorer) SetEncodedObject(object plumbing.EncodedObject) (plumbing.Hash, error) {
	return s.objects.SetEncodedObject(object)
}

func (s *quarantineStorer) EncodedObject(
	objectType plumbing.ObjectType,
	hash plumbing.Hash,
) (plumbing.EncodedObject, error) {
	return s.objects.EncodedObject(objectType, hash)
}

func (s *quarantineStorer) IterEncodedObjects(
	objectType plumbing.ObjectType,
) (storer.EncodedObjectIter, error) {
	return s.objects.IterEncodedObjects(objectType)
}

func (s *quarantineStorer) HasEncodedObject(hash plumbing.Hash) error {
	return s.objects.HasEncodedObject(hash)
}

func (s *quarantineStorer) EncodedObjectSize(hash plumbing.Hash) (int64, error) {
	return s.objects.EncodedObjectSize(hash)
}

func (s *quarantineStorer) AddAlternate(remote string) error {
	return s.objects.AddAlternate(remote)
}

func (s *quarantineStorer) PackfileWriter() (io.WriteCloser, error) {
	return s.packWriter.PackfileWriter()
}

func newReceiveQuarantine(
	repositoryPath string,
	base gitstorage.Storer,
) (*receiveQuarantine, error) {
	root, err := os.MkdirTemp(
		filepath.Dir(repositoryPath),
		"."+filepath.Base(repositoryPath)+".receive-",
	)
	if err != nil {
		return nil, fmt.Errorf("create receive quarantine: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(root)
		}
	}()

	temporary := filesystem.NewStorage(osfs.New(root), cache.NewObjectLRUDefault())
	if err = temporary.Init(); err != nil {
		return nil, fmt.Errorf("initialize receive quarantine: %w", err)
	}
	combined := &quarantineStorer{
		Storer:     base,
		objects:    transactional.NewObjectStorage(base, temporary),
		packWriter: temporary,
	}
	repository, err := git.Open(combined, nil)
	if err != nil {
		return nil, fmt.Errorf("open receive quarantine: %w", err)
	}

	cleanup = false
	return &receiveQuarantine{
		Root:       root,
		Repository: repository,
		temporary:  temporary,
	}, nil
}

func (q *receiveQuarantine) Remove() {
	_ = q.temporary.Close()
	_ = os.RemoveAll(q.Root)
}

func (q *receiveQuarantine) Promote(repositoryPath string) error {
	packs, err := q.temporary.ObjectPacks()
	if err != nil {
		return fmt.Errorf("list quarantined packs: %w", err)
	}
	if len(packs) == 0 {
		return nil
	}
	if len(packs) > 1 {
		return fmt.Errorf("receive quarantine contains %d packs, want one", len(packs))
	}
	if err = q.temporary.Close(); err != nil {
		return fmt.Errorf("close receive quarantine: %w", err)
	}

	hash := packs[0].String()
	sourceBase := filepath.Join(q.Root, "objects", "pack", "pack-"+hash)
	destinationBase := filepath.Join(repositoryPath, "objects", "pack", "pack-"+hash)
	sourceIndex := sourceBase + ".idx"
	sourcePack := sourceBase + ".pack"
	destinationIndex := destinationBase + ".idx"
	destinationPack := destinationBase + ".pack"

	indexExists, err := regularFileExists(destinationIndex)
	if err != nil {
		return err
	}
	packExists, err := regularFileExists(destinationPack)
	if err != nil {
		return err
	}
	if indexExists != packExists {
		return errors.New("live object storage contains an incomplete pack")
	}
	if indexExists {
		return nil
	}
	if err = requireRegularFile(sourceIndex); err != nil {
		return err
	}
	if err = requireRegularFile(sourcePack); err != nil {
		return err
	}

	// Publish the index first. Object discovery keys off the .pack file, so the
	// pack becomes visible only after both files are in their final location.
	if err = os.Rename(sourceIndex, destinationIndex); err != nil {
		return fmt.Errorf("publish pack index: %w", err)
	}
	if err = os.Rename(sourcePack, destinationPack); err != nil {
		rollbackErr := os.Rename(destinationIndex, sourceIndex)
		if rollbackErr != nil {
			_ = os.Remove(destinationIndex)
			return fmt.Errorf(
				"publish pack and roll back index: %w",
				errors.Join(err, rollbackErr),
			)
		}
		return fmt.Errorf("publish pack: %w", err)
	}
	return nil
}

func enforceRepositoryObjectQuota(repositoryPath, quarantinePath string, maximum int64) error {
	current, err := directoryRegularFileBytes(filepath.Join(repositoryPath, "objects"))
	if err != nil {
		return fmt.Errorf("measure repository object storage: %w", err)
	}
	incoming, err := directoryRegularFileBytes(filepath.Join(quarantinePath, "objects"))
	if err != nil {
		return fmt.Errorf("measure quarantined object storage: %w", err)
	}
	if current > maximum || incoming > maximum-current {
		return fmt.Errorf("repository Git object quota of %d bytes exceeded", maximum)
	}
	return nil
}

func directoryRegularFileBytes(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > math.MaxInt64-total {
			return errors.New("object storage size overflow")
		}
		total += info.Size()
		return nil
	})
	return total, err
}

func regularFileExists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect %s: %w", filepath.Base(path), err)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("%s is not a regular file", filepath.Base(path))
	}
	return true, nil
}

func requireRegularFile(path string) error {
	exists, err := regularFileExists(path)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%s is missing", filepath.Base(path))
	}
	return nil
}
