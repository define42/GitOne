package githttp

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/define42/GitOne/internal/gitformat"
	"github.com/go-git/go-git/v6/plumbing"
)

func TestQuarantineStorerDelegatesObjectOperations(t *testing.T) {
	repositoryPath := filepath.Join(t.TempDir(), "repository.git")
	repository, err := gitformat.Init(repositoryPath, true)
	if err != nil {
		t.Fatal(err)
	}
	quarantine, err := newReceiveQuarantine(repositoryPath, repository.Storer)
	if err != nil {
		t.Fatal(err)
	}
	defer quarantine.Remove()

	store, ok := quarantine.Repository.Storer.(*quarantineStorer)
	if !ok {
		t.Fatalf("quarantine storer type = %T", quarantine.Repository.Storer)
	}
	object := store.NewEncodedObject()
	object.SetType(plumbing.BlobObject)
	writer, err := object.Writer()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.WriteString(writer, "quarantined object"); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	hash, err := store.SetEncodedObject(object)
	if err != nil {
		t.Fatal(err)
	}
	if hash.Size() != gitSHA256ObjectIDSize {
		t.Fatalf("quarantine object ID uses %d bytes, want %d", hash.Size(), gitSHA256ObjectIDSize)
	}
	if err = store.HasEncodedObject(hash); err != nil {
		t.Fatal(err)
	}
	if size, sizeErr := store.EncodedObjectSize(hash); sizeErr != nil || size != int64(len("quarantined object")) {
		t.Fatalf("encoded object size = %d, %v", size, sizeErr)
	}
	loaded, err := store.EncodedObject(plumbing.BlobObject, hash)
	if err != nil || loaded.Hash() != hash {
		t.Fatalf("encoded object = %v, %v", loaded, err)
	}
	iterator, err := store.IterEncodedObjects(plumbing.BlobObject)
	if err != nil {
		t.Fatal(err)
	}
	defer iterator.Close()
	iterated, err := iterator.Next()
	if err != nil || iterated.Hash() != hash {
		t.Fatalf("iterated object = %v, %v", iterated, err)
	}
	if err = store.AddAlternate(filepath.Join(repositoryPath, "objects")); err != nil {
		t.Fatal(err)
	}

	// Loose objects remain quarantined but do not require pack publication.
	if err = quarantine.Promote(repositoryPath); err != nil {
		t.Fatal(err)
	}
}

func TestRegularFileChecks(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	if exists, err := regularFileExists(missing); err != nil || exists {
		t.Fatalf("missing file = %v, %v", exists, err)
	}
	if err := requireRegularFile(missing); err == nil {
		t.Fatal("missing required file was accepted")
	}
	directory := filepath.Join(root, "directory")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := regularFileExists(directory); err == nil {
		t.Fatal("directory was accepted as a regular file")
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("contents"), 0o640); err != nil {
		t.Fatal(err)
	}
	if exists, err := regularFileExists(file); err != nil || !exists {
		t.Fatalf("regular file = %v, %v", exists, err)
	}
	if err := requireRegularFile(file); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(root, "symlink")
	if err := os.Symlink(file, symlink); err != nil {
		t.Fatal(err)
	}
	if size, err := directoryRegularFileBytes(root); err != nil || size != int64(len("contents")) {
		t.Fatalf("regular file bytes = %d, %v", size, err)
	}
	notDirectory := filepath.Join(file, "child")
	if _, err := regularFileExists(notDirectory); err == nil {
		t.Fatal("path below a regular file did not return an inspection error")
	}
	if err := requireRegularFile(notDirectory); err == nil {
		t.Fatal("required path below a regular file did not return an inspection error")
	}
	if _, err := directoryRegularFileBytes(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing directory size error = %v", err)
	}
}

func TestRepositoryObjectQuotaReportsMeasurementBoundaries(t *testing.T) {
	root := t.TempDir()
	repository := filepath.Join(root, "repository.git")
	quarantine := filepath.Join(root, "quarantine")
	if err := enforceRepositoryObjectQuota(repository, quarantine, 1024); err == nil ||
		!strings.Contains(err.Error(), "measure repository") {
		t.Fatalf("missing repository objects returned %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repository, "objects"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := enforceRepositoryObjectQuota(repository, quarantine, 1024); err == nil ||
		!strings.Contains(err.Error(), "measure quarantined") {
		t.Fatalf("missing quarantine objects returned %v", err)
	}
}
