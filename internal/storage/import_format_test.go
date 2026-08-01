package storage

import (
	"bytes"
	"context"
	"crypto/sha1" // #nosec G505 -- availability probe for a legacy Git import test.
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/define42/GitOne/internal/gitformat"
	"github.com/define42/GitOne/internal/repopath"
	git "github.com/go-git/go-git/v6"
	gitconfig "github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	formatcfg "github.com/go-git/go-git/v6/plumbing/format/config"
	"github.com/go-git/go-git/v6/plumbing/object"
)

type legacySHA1ImportFixture struct {
	path       string
	blob       []byte
	blobHash   plumbing.Hash
	commitHash plumbing.Hash
}

func TestNormalizeImportedSHA256RepositoryUsesCleanBoundary(t *testing.T) {
	sourceStore := Store{Root: t.TempDir()}
	if err := sourceStore.CreateGroup("source", "alice", ""); err != nil {
		t.Fatal(err)
	}
	sourceName := repopath.Repository{Groups: []string{"source"}, Name: "api"}
	if err := sourceStore.CreateRepository(sourceName, CreateRepositoryOptions{
		InitializeReadme: true,
		Author:           "alice",
	}); err != nil {
		t.Fatal(err)
	}
	sourcePath, err := sourceStore.GitPath(sourceName)
	if err != nil {
		t.Fatal(err)
	}

	source, err := gitformat.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	sourceHead, err := source.Head()
	if err != nil {
		t.Fatal(err)
	}
	unreachable := source.Storer.NewEncodedObject()
	unreachable.SetType(plumbing.BlobObject)
	unreachableContents := []byte("unreachable import data\n")
	unreachable.SetSize(int64(len(unreachableContents)))
	unreachableWriter, err := unreachable.Writer()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = unreachableWriter.Write(unreachableContents); err != nil {
		_ = unreachableWriter.Close()
		t.Fatal(err)
	}
	if err = unreachableWriter.Close(); err != nil {
		t.Fatal(err)
	}
	unreachableHash, err := source.Storer.SetEncodedObject(unreachable)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = source.CreateRemote(&gitconfig.RemoteConfig{
		Name: "origin",
		URLs: []string{"https://credentials.example.invalid/source.git"},
	}); err != nil {
		t.Fatal(err)
	}
	if err = source.Close(); err != nil {
		t.Fatal(err)
	}

	for relative, contents := range map[string]string{
		"hooks/pre-receive": "#!/bin/sh\nexit 1\n",
		"logs/HEAD":         "sensitive reflog data\n",
		"private-metadata":  "must not cross the import boundary\n",
	} {
		path := filepath.Join(sourcePath, filepath.FromSlash(relative))
		if err = os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	destinationPath := filepath.Join(t.TempDir(), "normalized.git")
	normalized, err := normalizeImportedRepository(sourcePath, destinationPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := normalized.Close(); closeErr != nil {
			t.Errorf("close normalized repository: %v", closeErr)
		}
	}()
	if _, statErr := os.Lstat(sourcePath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("normalized source still exists: %v", statErr)
	}
	if err = gitformat.ValidateReachable(normalized); err != nil {
		t.Fatalf("validate normalized repository: %v", err)
	}
	normalizedHead, err := normalized.Head()
	if err != nil {
		t.Fatal(err)
	}
	if normalizedHead.Hash() != sourceHead.Hash() {
		t.Fatalf("normalized HEAD = %s, want %s", normalizedHead.Hash(), sourceHead.Hash())
	}
	if _, err = normalized.Storer.EncodedObject(plumbing.BlobObject, unreachableHash); !errors.Is(
		err,
		plumbing.ErrObjectNotFound,
	) {
		t.Fatalf("unreachable object survived clean import: %v", err)
	}
	if _, err = normalized.Remote("origin"); !errors.Is(err, git.ErrRemoteNotFound) {
		t.Fatalf("source remote survived clean import: %v", err)
	}
	for _, relative := range []string{"hooks/pre-receive", "logs/HEAD", "private-metadata"} {
		if _, statErr := os.Lstat(filepath.Join(destinationPath, filepath.FromSlash(relative))); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("source-only file %q survived clean import: %v", relative, statErr)
		}
	}
}

func TestNormalizeImportedRepositoryFailsClosed(t *testing.T) {
	t.Run("same source and destination", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "repository.git")
		repository, err := gitformat.Init(path, true)
		if err != nil {
			t.Fatal(err)
		}
		if err = repository.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err = normalizeImportedRepository(path, filepath.Clean(path)); err == nil ||
			!strings.Contains(err.Error(), "must differ") {
			t.Fatalf("same-path normalization error = %v", err)
		}
	})

	t.Run("malformed source", func(t *testing.T) {
		sourcePath := filepath.Join(t.TempDir(), "source.git")
		if err := os.MkdirAll(sourcePath, 0o750); err != nil {
			t.Fatal(err)
		}
		destinationPath := filepath.Join(t.TempDir(), "destination.git")
		_, err := normalizeImportedRepository(sourcePath, destinationPath)
		if err == nil || !strings.Contains(err.Error(), "inspect imported repository object format") {
			t.Fatalf("malformed-source normalization error = %v", err)
		}
		if _, statErr := os.Lstat(destinationPath); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("malformed source created destination: %v", statErr)
		}
	})

	t.Run("missing reachable object", func(t *testing.T) {
		sourcePath := filepath.Join(t.TempDir(), "source.git")
		repository, err := gitformat.Init(sourcePath, true)
		if err != nil {
			t.Fatal(err)
		}
		main := plumbing.NewBranchReferenceName("main")
		missing := plumbing.NewHash(strings.Repeat("1", formatcfg.SHA256HexSize))
		if err = repository.Storer.SetReference(plumbing.NewHashReference(main, missing)); err != nil {
			t.Fatal(err)
		}
		if err = repository.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, main)); err != nil {
			t.Fatal(err)
		}
		if err = repository.Close(); err != nil {
			t.Fatal(err)
		}

		destinationPath := filepath.Join(t.TempDir(), "destination.git")
		_, err = normalizeImportedRepository(sourcePath, destinationPath)
		if err == nil || !strings.Contains(err.Error(), "copy imported SHA-256 repository") {
			t.Fatalf("missing-object normalization error = %v", err)
		}
		if _, statErr := os.Lstat(sourcePath); statErr != nil {
			t.Fatalf("failed normalization removed source: %v", statErr)
		}
		if _, statErr := os.Lstat(destinationPath); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("failed normalization left destination: %v", statErr)
		}
	})
}

func TestNormalizeImportedRepositoryStrictRejectsSHA1BeforeOpen(t *testing.T) {
	if err := gitformat.RequireLegacySHA1(); err == nil {
		t.Skip("strict fips140=only mode is not active")
	}
	sourcePath := filepath.Join(t.TempDir(), "source.git")
	if err := os.MkdirAll(sourcePath, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(sourcePath, "config"),
		[]byte("[core]\n\tbare = true\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	destinationPath := filepath.Join(t.TempDir(), "destination.git")
	_, err := normalizeImportedRepository(sourcePath, destinationPath)
	if !errors.Is(err, gitformat.ErrLegacySHA1Unavailable) {
		t.Fatalf("normalizeImportedRepository error = %v, want ErrLegacySHA1Unavailable", err)
	}
	if _, statErr := os.Lstat(destinationPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("strict rejection created destination: %v", statErr)
	}
}

func TestImportRepositoryConvertsLegacySHA1(t *testing.T) {
	requireLegacySHA1Import(t)

	legacy := createLegacySHA1BareRepository(
		t,
		filepath.Join(t.TempDir(), "legacy.git"),
	)
	store, target := legacyImportDestination(t, "remote-import")
	if err := store.ImportRepository(
		context.Background(),
		target,
		ImportRepositoryOptions{URL: legacy.path},
	); err != nil {
		t.Fatal(err)
	}

	assertConvertedLegacyRepository(t, store, target, legacy)
	assertLegacySourceUnchanged(t, legacy)
	assertImportStagingEmpty(t, store)
}

func TestImportRepositoryArchiveConvertsLegacySHA1(t *testing.T) {
	requireLegacySHA1Import(t)

	legacy := createLegacySHA1BareRepository(
		t,
		filepath.Join(t.TempDir(), "legacy.git"),
	)
	archivePath := filepath.Join(t.TempDir(), "legacy.tar")
	writeRepositoryTAR(t, legacy.path, archivePath, "legacy.git")

	store, target := legacyImportDestination(t, "archive-import")
	if err := store.ImportRepositoryArchive(
		context.Background(),
		target,
		"legacy.tar",
		archivePath,
	); err != nil {
		t.Fatal(err)
	}

	assertConvertedLegacyRepository(t, store, target, legacy)
	assertLegacySourceUnchanged(t, legacy)
	assertImportStagingEmpty(t, store)
}

func requireLegacySHA1Import(t *testing.T) {
	t.Helper()
	if !standardLibrarySHA1Available() {
		t.Skip("strict fips140=only mode disables SHA-1 validation for legacy imports")
	}
}

func standardLibrarySHA1Available() (available bool) {
	defer func() {
		if recover() != nil {
			available = false
		}
	}()
	hasher := sha1.New() // #nosec G401 -- probe only; no security decision uses SHA-1.
	_, err := hasher.Write(nil)
	return err == nil
}

func createLegacySHA1BareRepository(
	t *testing.T,
	path string,
) legacySHA1ImportFixture {
	t.Helper()
	repository, err := git.PlainInit(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := repository.Close(); closeErr != nil {
			t.Errorf("close legacy repository: %v", closeErr)
		}
	}()

	objectFormat, err := gitformat.ObjectFormat(repository)
	if err != nil {
		t.Fatal(err)
	}
	if objectFormat != formatcfg.SHA1 {
		t.Fatalf("legacy source format = %s, want sha1", objectFormat)
	}

	blobContents := []byte("legacy payload\nwith exact bytes\x00\xff\n")
	blob := repository.Storer.NewEncodedObject()
	blob.SetType(plumbing.BlobObject)
	blob.SetSize(int64(len(blobContents)))
	blobWriter, err := blob.Writer()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = blobWriter.Write(blobContents); err != nil {
		_ = blobWriter.Close()
		t.Fatal(err)
	}
	if err = blobWriter.Close(); err != nil {
		t.Fatal(err)
	}
	blobHash, err := repository.Storer.SetEncodedObject(blob)
	if err != nil {
		t.Fatal(err)
	}

	tree := &object.Tree{Entries: []object.TreeEntry{{
		Name: "payload.bin",
		Mode: filemode.Regular,
		Hash: blobHash,
	}}}
	encodedTree := repository.Storer.NewEncodedObject()
	if err = tree.Encode(encodedTree); err != nil {
		t.Fatal(err)
	}
	treeHash, err := repository.Storer.SetEncodedObject(encodedTree)
	if err != nil {
		t.Fatal(err)
	}

	signature := object.Signature{
		Name:  "Legacy Author",
		Email: "legacy@example.test",
		When:  time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC),
	}
	commit := &object.Commit{
		Author:    signature,
		Committer: signature,
		Message:   "Legacy SHA-1 commit\n",
		TreeHash:  treeHash,
	}
	encodedCommit := repository.Storer.NewEncodedObject()
	if err = commit.Encode(encodedCommit); err != nil {
		t.Fatal(err)
	}
	commitHash, err := repository.Storer.SetEncodedObject(encodedCommit)
	if err != nil {
		t.Fatal(err)
	}

	main := plumbing.NewBranchReferenceName("main")
	for _, reference := range []*plumbing.Reference{
		plumbing.NewHashReference(main, commitHash),
		plumbing.NewHashReference(plumbing.NewTagReferenceName("legacy-v1"), commitHash),
		plumbing.NewSymbolicReference(plumbing.HEAD, main),
	} {
		if err = repository.Storer.SetReference(reference); err != nil {
			t.Fatal(err)
		}
	}
	if len(blobHash.String()) != formatcfg.SHA1HexSize ||
		len(commitHash.String()) != formatcfg.SHA1HexSize {
		t.Fatalf("legacy objects are not SHA-1: blob=%s commit=%s", blobHash, commitHash)
	}

	return legacySHA1ImportFixture{
		path:       path,
		blob:       blobContents,
		blobHash:   blobHash,
		commitHash: commitHash,
	}
}

func legacyImportDestination(t *testing.T, name string) (Store, repopath.Repository) {
	t.Helper()
	store := Store{Root: t.TempDir()}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	return store, repopath.Repository{Groups: []string{"engineering"}, Name: name}
}

func assertConvertedLegacyRepository(
	t *testing.T,
	store Store,
	target repopath.Repository,
	legacy legacySHA1ImportFixture,
) {
	t.Helper()
	path, err := store.GitPath(target)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := gitformat.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := repository.Close(); closeErr != nil {
			t.Errorf("close converted repository: %v", closeErr)
		}
	}()
	if err = gitformat.Validate(repository); err != nil {
		t.Fatalf("validate converted repository: %v", err)
	}
	if err = gitformat.ValidateReachable(repository); err != nil {
		t.Fatalf("fully validate converted repository: %v", err)
	}

	mainName := plumbing.NewBranchReferenceName("main")
	main, err := repository.Reference(mainName, false)
	if err != nil {
		t.Fatal(err)
	}
	tag, err := repository.Reference(plumbing.NewTagReferenceName("legacy-v1"), false)
	if err != nil {
		t.Fatal(err)
	}
	head, err := repository.Reference(plumbing.HEAD, false)
	if err != nil {
		t.Fatal(err)
	}
	if head.Type() != plumbing.SymbolicReference || head.Target() != mainName {
		t.Fatalf("converted HEAD = %s, want symbolic %s", head, mainName)
	}
	if main.Hash() != tag.Hash() {
		t.Fatalf("converted refs disagree: main=%s tag=%s", main.Hash(), tag.Hash())
	}
	if main.Hash().String() == legacy.commitHash.String() ||
		!gitformat.IsSHA256OID(main.Hash().String()) {
		t.Fatalf("main was not remapped to SHA-256: old=%s new=%s", legacy.commitHash, main.Hash())
	}

	hashReferences := 0
	references, err := repository.References()
	if err != nil {
		t.Fatal(err)
	}
	if err = references.ForEach(func(reference *plumbing.Reference) error {
		if reference.Type() != plumbing.HashReference {
			return nil
		}
		hashReferences++
		if !gitformat.IsSHA256OID(reference.Hash().String()) {
			t.Errorf("converted reference %s has non-SHA-256 ID %s", reference.Name(), reference.Hash())
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if hashReferences < 2 {
		t.Fatalf("converted hash references = %d, want at least 2", hashReferences)
	}

	convertedCommit, err := repository.CommitObject(main.Hash())
	if err != nil {
		t.Fatal(err)
	}
	convertedTree, err := convertedCommit.Tree()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := convertedTree.File("payload.bin")
	if err != nil {
		t.Fatal(err)
	}
	contents, err := payload.Contents()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal([]byte(contents), legacy.blob) {
		t.Fatalf("converted blob bytes = %q, want %q", []byte(contents), legacy.blob)
	}
	entry, err := convertedTree.FindEntry("payload.bin")
	if err != nil {
		t.Fatal(err)
	}
	if entry.Hash.String() == legacy.blobHash.String() ||
		!gitformat.IsSHA256OID(entry.Hash.String()) {
		t.Fatalf("blob was not remapped to SHA-256: old=%s new=%s", legacy.blobHash, entry.Hash)
	}
}

func assertLegacySourceUnchanged(t *testing.T, legacy legacySHA1ImportFixture) {
	t.Helper()
	repository, err := git.PlainOpen(legacy.path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repository.Close() }()
	main, err := repository.Reference(plumbing.NewBranchReferenceName("main"), false)
	if err != nil {
		t.Fatal(err)
	}
	if main.Hash() != legacy.commitHash {
		t.Fatalf("legacy source changed: main=%s want=%s", main.Hash(), legacy.commitHash)
	}
	objectFormat, err := gitformat.ObjectFormat(repository)
	if err != nil {
		t.Fatal(err)
	}
	if objectFormat != formatcfg.SHA1 {
		t.Fatalf("legacy source format changed to %s", objectFormat)
	}
}

func assertImportStagingEmpty(t *testing.T, store Store) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(store.Root, ".gitone", "imports"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("successful import left %d staging entries", len(entries))
	}
}
