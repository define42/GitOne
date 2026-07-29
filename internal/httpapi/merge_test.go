package httpapi

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/define42/GitOne/internal/auth"
	"github.com/define42/GitOne/internal/control"
	"github.com/define42/GitOne/internal/repopath"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	gitstorage "github.com/go-git/go-git/v5/storage"
)

func storeTestBlob(t *testing.T, repository *git.Repository, content []byte) plumbing.Hash {
	t.Helper()
	encoded := &plumbing.MemoryObject{}
	encoded.SetType(plumbing.BlobObject)
	encoded.SetSize(int64(len(content)))
	writer, err := encoded.Writer()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Write(content); err != nil {
		_ = writer.Close()
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	hash, err := repository.Storer.SetEncodedObject(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func storeTestTree(
	t *testing.T,
	repository *git.Repository,
	entries ...object.TreeEntry,
) *object.Tree {
	t.Helper()
	sort.Sort(object.TreeEntrySorter(entries))
	tree := &object.Tree{Entries: entries}
	encoded := &plumbing.MemoryObject{}
	if err := tree.Encode(encoded); err != nil {
		t.Fatal(err)
	}
	hash, err := repository.Storer.SetEncodedObject(encoded)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := repository.TreeObject(hash)
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

func storeTestCommit(
	t *testing.T,
	repository *git.Repository,
	tree *object.Tree,
	parents ...plumbing.Hash,
) *object.Commit {
	t.Helper()
	signature := object.Signature{
		Name:  "alice",
		Email: "alice@example.com",
		When:  time.Unix(1, 0).UTC(),
	}
	commit := &object.Commit{
		Author:       signature,
		Committer:    signature,
		Message:      "test commit",
		TreeHash:     tree.Hash,
		ParentHashes: parents,
	}
	encoded := &plumbing.MemoryObject{}
	if err := commit.Encode(encoded); err != nil {
		t.Fatal(err)
	}
	hash, err := repository.Storer.SetEncodedObject(encoded)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := repository.CommitObject(hash)
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

func TestMergeRepositoryBranchesAtSourceRejectsMovedSource(t *testing.T) {
	repository, err := git.PlainInit(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	base := storeTestCommit(t, repository, storeTestTree(
		t,
		repository,
		object.TreeEntry{
			Name: "notes.txt",
			Mode: filemode.Regular,
			Hash: storeTestBlob(t, repository, []byte("base\n")),
		},
	))
	firstSource := storeTestCommit(t, repository, storeTestTree(
		t,
		repository,
		object.TreeEntry{
			Name: "notes.txt",
			Mode: filemode.Regular,
			Hash: storeTestBlob(t, repository, []byte("first\n")),
		},
	), base.Hash)
	currentSource := storeTestCommit(t, repository, storeTestTree(
		t,
		repository,
		object.TreeEntry{
			Name: "notes.txt",
			Mode: filemode.Regular,
			Hash: storeTestBlob(t, repository, []byte("current\n")),
		},
	), firstSource.Hash)
	targetReference := plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("main"),
		base.Hash,
	)
	if err = repository.Storer.SetReference(targetReference); err != nil {
		t.Fatal(err)
	}
	if err = repository.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("feature"),
		currentSource.Hash,
	)); err != nil {
		t.Fatal(err)
	}

	parsed := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	service := API{}
	_, err = service.mergeRepositoryBranchesAtSource(
		repository,
		parsed,
		"main",
		"feature",
		"alice",
		"",
		firstSource.Hash.String(),
		nil,
	)
	var statusError huma.StatusError
	if !errors.As(err, &statusError) || statusError.GetStatus() != http.StatusConflict {
		t.Fatalf("moved source returned %v, want HTTP 409", err)
	}
	unchanged, err := repository.Reference(targetReference.Name(), false)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Hash() != base.Hash {
		t.Fatalf("stale approval moved target to %s", unchanged.Hash())
	}

	var planned repositoryMergeResult
	result, err := service.mergeRepositoryBranchesAtSource(
		repository,
		parsed,
		"main",
		"feature",
		"alice",
		"",
		currentSource.Hash.String(),
		func(plan repositoryMergeResult) error {
			beforeUpdate, referenceErr := repository.Reference(targetReference.Name(), false)
			if referenceErr != nil {
				return referenceErr
			}
			if beforeUpdate.Hash() != base.Hash {
				t.Errorf("target moved before the merge plan was persisted")
			}
			planned = plan
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Repository != parsed.Full() ||
		result.Target != "main" ||
		result.Source != "feature" ||
		result.Commit != currentSource.Hash.String() ||
		result.Strategy != "fast-forward" {
		t.Fatalf("unexpected merge result: %#v", result)
	}
	if planned.PreviousTarget != base.Hash.String() ||
		planned.Commit != currentSource.Hash.String() ||
		planned.Strategy != "fast-forward" {
		t.Fatalf("unexpected persisted merge plan: %#v", planned)
	}
	updated, err := repository.Reference(targetReference.Name(), false)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Hash() != currentSource.Hash {
		t.Fatalf("target = %s, want %s", updated.Hash(), currentSource.Hash)
	}
}

func TestTargetReferenceUpdateErrorMarksOnlyKnownCASConflict(t *testing.T) {
	err := targetReferenceUpdateError(gitstorage.ErrReferenceHasChanged)
	var notApplied *mergeNotAppliedError
	if !errors.As(err, &notApplied) {
		t.Fatal("reference mismatch was not marked as conclusively unapplied")
	}
	var statusError huma.StatusError
	if !errors.As(err, &statusError) || statusError.GetStatus() != http.StatusConflict {
		t.Fatalf("marked conflict lost its HTTP status: %v", err)
	}

	err = targetReferenceUpdateError(errors.New("ambiguous storage failure"))
	notApplied = nil
	if errors.As(err, &notApplied) {
		t.Fatal("ambiguous reference failure was marked as conclusively unapplied")
	}
}

func TestCompareCanMergeWithRepositoryScopedWriteToken(t *testing.T) {
	service, _, head := repositoryAPIFixture(t)
	parsed := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	repositoryPath, err := service.Storage.GitPath(parsed)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := git.PlainOpen(repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = repository.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("feature"),
		plumbing.NewHash(head),
	)); err != nil {
		t.Fatal(err)
	}
	document, err := service.Resolver.Controls.Load(context.Background(), parsed.Group())
	if err != nil {
		t.Fatal(err)
	}
	tokenHash, err := auth.HashSecret("token-secret")
	if err != nil {
		t.Fatal(err)
	}
	document.Tokens = append(document.Tokens, control.Token{
		Name: "review automation",
		Key:  "reviewer",
		Hash: tokenHash,
		Role: control.RoleDeveloper,
	})
	if err = service.Storage.UpdateGroupControl(parsed.Group(), document, "alice"); err != nil {
		t.Fatal(err)
	}
	service.Resolver.Controls.Invalidate(parsed.Group())
	request, err := http.NewRequest(http.MethodGet, "/", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.SetBasicAuth("reviewer", "token-secret")
	credentials := AuthInput{
		Authorization: request.Header.Get("Authorization"),
	}

	comparison, err := service.compareRepositoryBranches(
		context.Background(),
		&compareRepositoryBranchesInput{
			AuthInput:  credentials,
			Repository: parsed.Full(),
			Base:       "main",
			Head:       "feature",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !comparison.Body.CanMerge {
		t.Fatal("group write token was not allowed to merge")
	}
	branches, err := service.listRepositoryBranches(
		context.Background(),
		&repositoryBranchesInput{
			AuthInput:  credentials,
			Repository: parsed.Full(),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !branches.Body.CanWrite {
		t.Fatal("group write token was not reported as writable")
	}
}

func TestMergeTextLines(t *testing.T) {
	base := "one\ntwo\nthree\nfour\n"
	target := "one\ntarget\ntwo\nthree\nfour\n"
	source := "one\ntwo\nthree\nsource\n"

	merged, clean := mergeTextLines(base, target, source)
	if !clean {
		t.Fatal("independent line changes were reported as conflicting")
	}
	expected := "one\ntarget\ntwo\nthree\nsource\n"
	if merged != expected {
		t.Fatalf("unexpected merged text:\n%s", merged)
	}
}

func TestMergeTextLinesRejectsConflict(t *testing.T) {
	base := "one\ntwo\nthree\n"
	target := "one\ntarget\nthree\n"
	source := "one\nsource\nthree\n"

	if merged, clean := mergeTextLines(base, target, source); clean {
		t.Fatalf("overlapping line changes unexpectedly merged as %q", merged)
	}
}

func TestMergeTextLinesAcceptsIdenticalChange(t *testing.T) {
	base := "one\ntwo\n"
	version := "one\nchanged\n"

	merged, clean := mergeTextLines(base, version, version)
	if !clean || merged != version {
		t.Fatalf("identical changes did not merge cleanly: clean=%v merged=%q", clean, merged)
	}
}

func TestMergeFileMode(t *testing.T) {
	for _, test := range []struct {
		name                 string
		base, target, source filemode.FileMode
		want                 filemode.FileMode
		clean                bool
	}{
		{
			name:   "same result",
			base:   filemode.Regular,
			target: filemode.Executable,
			source: filemode.Executable,
			want:   filemode.Executable,
			clean:  true,
		},
		{
			name:   "source changes",
			base:   filemode.Regular,
			target: filemode.Regular,
			source: filemode.Executable,
			want:   filemode.Executable,
			clean:  true,
		},
		{
			name:   "target changes",
			base:   filemode.Regular,
			target: filemode.Executable,
			source: filemode.Regular,
			want:   filemode.Executable,
			clean:  true,
		},
		{
			name:   "conflicting changes",
			base:   filemode.Regular,
			target: filemode.Executable,
			source: filemode.Symlink,
			want:   filemode.Empty,
			clean:  false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			mode, clean := mergeFileMode(test.base, test.target, test.source)
			if mode != test.want || clean != test.clean {
				t.Fatalf("mergeFileMode() = (%s, %v), want (%s, %v)", mode, clean, test.want, test.clean)
			}
		})
	}
}

func TestMergeFileEntriesWithStoredGitBlobs(t *testing.T) {
	repository, err := git.PlainInit(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	storeBlob := func(content []byte) plumbing.Hash {
		t.Helper()
		encoded := &plumbing.MemoryObject{}
		encoded.SetType(plumbing.BlobObject)
		encoded.SetSize(int64(len(content)))
		writer, writerErr := encoded.Writer()
		if writerErr != nil {
			t.Fatal(writerErr)
		}
		if _, writerErr = writer.Write(content); writerErr != nil {
			_ = writer.Close()
			t.Fatal(writerErr)
		}
		if writerErr = writer.Close(); writerErr != nil {
			t.Fatal(writerErr)
		}
		hash, storeErr := repository.Storer.SetEncodedObject(encoded)
		if storeErr != nil {
			t.Fatal(storeErr)
		}
		return hash
	}
	entry := func(hash plumbing.Hash, mode filemode.FileMode) *object.TreeEntry {
		return &object.TreeEntry{Name: "notes.txt", Mode: mode, Hash: hash}
	}

	baseHash := storeBlob([]byte("one\ntwo\nthree\n"))
	targetHash := storeBlob([]byte("ONE\ntwo\nthree\n"))
	sourceHash := storeBlob([]byte("one\ntwo\nTHREE\n"))
	base := entry(baseHash, filemode.Regular)
	target := entry(targetHash, filemode.Regular)
	source := entry(sourceHash, filemode.Regular)

	preview, clean, err := mergeFileEntries(repository, base, target, source, false)
	if err != nil || !clean {
		t.Fatalf("preview independent changes: clean=%v err=%v", clean, err)
	}
	if _, err = repository.BlobObject(preview.Hash); err == nil {
		t.Fatal("preview persisted its merged blob")
	}

	persisted, clean, err := mergeFileEntries(repository, base, target, source, true)
	if err != nil || !clean {
		t.Fatalf("persist independent changes: clean=%v err=%v", clean, err)
	}
	merged, err := readMergeBlob(repository, persisted.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if string(merged) != "ONE\ntwo\nTHREE\n" {
		t.Fatalf("unexpected merged content: %q", merged)
	}

	conflictingSource := entry(
		storeBlob([]byte("SOURCE\ntwo\nthree\n")),
		filemode.Regular,
	)
	if _, clean, err = mergeFileEntries(
		repository,
		base,
		target,
		conflictingSource,
		true,
	); err != nil || clean {
		t.Fatalf("conflicting blobs returned clean=%v err=%v", clean, err)
	}

	binaryBase := entry(storeBlob([]byte{'a', 0, 'b'}), filemode.Regular)
	binaryTarget := entry(storeBlob([]byte{'c', 0, 'd'}), filemode.Regular)
	binarySource := entry(storeBlob([]byte{'e', 0, 'f'}), filemode.Regular)
	if _, clean, err = mergeFileEntries(
		repository,
		binaryBase,
		binaryTarget,
		binarySource,
		true,
	); err != nil || clean {
		t.Fatalf("binary blobs returned clean=%v err=%v", clean, err)
	}

	if _, clean, err = mergeFileEntries(
		repository,
		base,
		nil,
		source,
		true,
	); err != nil || clean {
		t.Fatalf("missing target returned clean=%v err=%v", clean, err)
	}

	missingBlob := entry(
		plumbing.NewHash("4444444444444444444444444444444444444444"),
		filemode.Regular,
	)
	if _, _, err = mergeFileEntries(
		repository,
		missingBlob,
		target,
		source,
		true,
	); err == nil || errors.Is(err, errUnmergeableBlob) {
		t.Fatalf("missing base blob returned %v", err)
	}
}

func TestMergeTreesWithStoredGitObjects(t *testing.T) {
	repository, err := git.PlainInit(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	storeBlob := func(content string) plumbing.Hash {
		t.Helper()
		encoded := &plumbing.MemoryObject{}
		encoded.SetType(plumbing.BlobObject)
		encoded.SetSize(int64(len(content)))
		writer, writerErr := encoded.Writer()
		if writerErr != nil {
			t.Fatal(writerErr)
		}
		if _, writerErr = writer.Write([]byte(content)); writerErr != nil {
			_ = writer.Close()
			t.Fatal(writerErr)
		}
		if writerErr = writer.Close(); writerErr != nil {
			t.Fatal(writerErr)
		}
		hash, storeErr := repository.Storer.SetEncodedObject(encoded)
		if storeErr != nil {
			t.Fatal(storeErr)
		}
		return hash
	}
	storeTree := func(entries []object.TreeEntry) *object.Tree {
		t.Helper()
		sort.Sort(object.TreeEntrySorter(entries))
		tree := &object.Tree{Entries: entries}
		encoded := &plumbing.MemoryObject{}
		if encodeErr := tree.Encode(encoded); encodeErr != nil {
			t.Fatal(encodeErr)
		}
		hash, storeErr := repository.Storer.SetEncodedObject(encoded)
		if storeErr != nil {
			t.Fatal(storeErr)
		}
		stored, loadErr := repository.TreeObject(hash)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		return stored
	}
	fileEntry := func(name string, hash plumbing.Hash) object.TreeEntry {
		return object.TreeEntry{Name: name, Mode: filemode.Regular, Hash: hash}
	}
	dirEntry := func(name string, tree *object.Tree) object.TreeEntry {
		return object.TreeEntry{Name: name, Mode: filemode.Dir, Hash: tree.Hash}
	}

	baseHash := storeBlob("one\ntwo\nthree\n")
	targetHash := storeBlob("ONE\ntwo\nthree\n")
	sourceHash := storeBlob("one\ntwo\nTHREE\n")
	targetNewHash := storeBlob("target addition\n")
	sourceNewHash := storeBlob("source addition\n")
	baseNested := storeTree([]object.TreeEntry{fileEntry("note.txt", baseHash)})
	targetNested := storeTree([]object.TreeEntry{fileEntry("note.txt", targetHash)})
	sourceNested := storeTree([]object.TreeEntry{fileEntry("note.txt", sourceHash)})

	base := storeTree([]object.TreeEntry{
		dirEntry("docs", baseNested),
		fileEntry("same.txt", baseHash),
		fileEntry("source-change.txt", baseHash),
		fileEntry("source-delete.txt", baseHash),
		fileEntry("target-change.txt", baseHash),
		fileEntry("target-delete.txt", baseHash),
	})
	target := storeTree([]object.TreeEntry{
		dirEntry("docs", targetNested),
		fileEntry("same.txt", baseHash),
		fileEntry("source-change.txt", baseHash),
		fileEntry("source-delete.txt", baseHash),
		fileEntry("target-change.txt", targetHash),
		fileEntry("target-new.txt", targetNewHash),
	})
	source := storeTree([]object.TreeEntry{
		dirEntry("docs", sourceNested),
		fileEntry("same.txt", baseHash),
		fileEntry("source-change.txt", sourceHash),
		fileEntry("source-new.txt", sourceNewHash),
		fileEntry("target-change.txt", baseHash),
		fileEntry("target-delete.txt", baseHash),
	})

	previewHash, conflicts, err := mergeTrees(repository, base, target, source, "", false)
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("preview merge: conflicts=%v err=%v", conflicts, err)
	}
	if _, err = repository.TreeObject(previewHash); err == nil {
		t.Fatal("preview merge persisted its tree")
	}

	mergedHash, conflicts, err := mergeTrees(repository, base, target, source, "", true)
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("persist merge: conflicts=%v err=%v", conflicts, err)
	}
	merged, err := repository.TreeObject(mergedHash)
	if err != nil {
		t.Fatal(err)
	}
	mergedEntries := treeEntries(merged)
	for name, wantHash := range map[string]plumbing.Hash{
		"same.txt":          baseHash,
		"source-change.txt": sourceHash,
		"source-new.txt":    sourceNewHash,
		"target-change.txt": targetHash,
		"target-new.txt":    targetNewHash,
	} {
		entry := mergedEntries[name]
		if entry == nil || entry.Hash != wantHash {
			t.Fatalf("merged entry %q = %#v, want hash %s", name, entry, wantHash)
		}
	}
	for _, name := range []string{"source-delete.txt", "target-delete.txt"} {
		if mergedEntries[name] != nil {
			t.Fatalf("deleted entry %q remains in merged tree", name)
		}
	}
	docs, err := repository.TreeObject(mergedEntries["docs"].Hash)
	if err != nil {
		t.Fatal(err)
	}
	note := treeEntries(docs)["note.txt"]
	noteContent, err := readMergeBlob(repository, note.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if string(noteContent) != "ONE\ntwo\nTHREE\n" {
		t.Fatalf("nested merge content = %q", noteContent)
	}

	conflictingSourceHash := storeBlob("SOURCE\ntwo\nthree\n")
	conflictingSourceTree := storeTree([]object.TreeEntry{
		fileEntry("note.txt", conflictingSourceHash),
	})
	fileKindHash := storeBlob("plain file\n")
	dirKindTree := storeTree([]object.TreeEntry{fileEntry("nested.txt", baseHash)})
	conflictBase := storeTree([]object.TreeEntry{
		dirEntry("docs", baseNested),
	})
	conflictTarget := storeTree([]object.TreeEntry{
		dirEntry("docs", targetNested),
		fileEntry("kind", fileKindHash),
	})
	conflictSource := storeTree([]object.TreeEntry{
		dirEntry("docs", conflictingSourceTree),
		dirEntry("kind", dirKindTree),
	})
	conflictHash, conflicts, err := mergeTrees(
		repository,
		conflictBase,
		conflictTarget,
		conflictSource,
		"",
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 2 ||
		conflicts[0] != "docs/note.txt" ||
		conflicts[1] != "kind" {
		t.Fatalf("unexpected tree conflicts: %v", conflicts)
	}
	if _, err = repository.TreeObject(conflictHash); err == nil {
		t.Fatal("conflicting merge persisted its tree")
	}
}

func TestAssessBranchMergeWithStoredCommitGraphs(t *testing.T) {
	repository, err := git.PlainInit(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	empty := storeTestTree(t, repository)
	root := storeTestCommit(t, repository, empty)
	target := storeTestCommit(t, repository, empty, root.Hash)
	branchBlob := storeTestBlob(t, repository, []byte("source branch\n"))
	sourceTreeOnly := storeTestTree(t, repository, object.TreeEntry{
		Name: "source.txt", Mode: filemode.Regular, Hash: branchBlob,
	})
	source := storeTestCommit(t, repository, sourceTreeOnly, root.Hash)

	base, clean, conflicts, err := assessBranchMerge(repository, root, root)
	if err != nil || !clean || base.Hash != root.Hash || len(conflicts) != 0 {
		t.Fatalf("same commit assessment = %v, %v, %v, %v", base, clean, conflicts, err)
	}
	base, clean, conflicts, err = assessBranchMerge(repository, target, root)
	if err != nil || !clean || base.Hash != root.Hash || len(conflicts) != 0 {
		t.Fatalf("source ancestor assessment = %v, %v, %v, %v", base, clean, conflicts, err)
	}
	base, clean, conflicts, err = assessBranchMerge(repository, root, source)
	if err != nil || !clean || base.Hash != root.Hash || len(conflicts) != 0 {
		t.Fatalf("target ancestor assessment = %v, %v, %v, %v", base, clean, conflicts, err)
	}
	base, clean, conflicts, err = assessBranchMerge(repository, target, source)
	if err != nil || !clean || base.Hash != root.Hash || len(conflicts) != 0 {
		t.Fatalf("clean divergence assessment = %v, %v, %v, %v", base, clean, conflicts, err)
	}

	unrelated := storeTestCommit(t, repository, sourceTreeOnly)
	base, clean, conflicts, err = assessBranchMerge(repository, target, unrelated)
	if err != nil || clean || base != nil ||
		len(conflicts) != 1 || conflicts[0] != "No single merge base" {
		t.Fatalf("unrelated history assessment = %v, %v, %v, %v", base, clean, conflicts, err)
	}

	baseBlob := storeTestBlob(t, repository, []byte("base\n"))
	targetBlob := storeTestBlob(t, repository, []byte("target\n"))
	sourceBlob := storeTestBlob(t, repository, []byte("source\n"))
	baseTree := storeTestTree(t, repository, object.TreeEntry{
		Name: "notes.txt", Mode: filemode.Regular, Hash: baseBlob,
	})
	targetTree := storeTestTree(t, repository, object.TreeEntry{
		Name: "notes.txt", Mode: filemode.Regular, Hash: targetBlob,
	})
	sourceTree := storeTestTree(t, repository, object.TreeEntry{
		Name: "notes.txt", Mode: filemode.Regular, Hash: sourceBlob,
	})
	conflictRoot := storeTestCommit(t, repository, baseTree)
	conflictTarget := storeTestCommit(t, repository, targetTree, conflictRoot.Hash)
	conflictSource := storeTestCommit(t, repository, sourceTree, conflictRoot.Hash)
	base, clean, conflicts, err = assessBranchMerge(
		repository,
		conflictTarget,
		conflictSource,
	)
	if err != nil || clean || base.Hash != conflictRoot.Hash ||
		len(conflicts) != 1 || conflicts[0] != "notes.txt" {
		t.Fatalf("conflict assessment = %v, %v, %v, %v", base, clean, conflicts, err)
	}
}

func TestCompareTreesReportsStoredFileChanges(t *testing.T) {
	repository, err := git.PlainInit(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	original := storeTestBlob(t, repository, []byte("one\ntwo\n"))
	modified := storeTestBlob(t, repository, []byte("one\nchanged\nthree"))
	added := storeTestBlob(t, repository, []byte("new\n"))
	binaryBefore := storeTestBlob(t, repository, []byte{'a', 0, 'b'})
	binaryAfter := storeTestBlob(t, repository, []byte{'c', 0, 'd'})
	from := storeTestTree(
		t,
		repository,
		object.TreeEntry{Name: "deleted.txt", Mode: filemode.Regular, Hash: original},
		object.TreeEntry{Name: "modified.txt", Mode: filemode.Regular, Hash: original},
		object.TreeEntry{Name: "binary.dat", Mode: filemode.Regular, Hash: binaryBefore},
	)
	to := storeTestTree(
		t,
		repository,
		object.TreeEntry{Name: "added.txt", Mode: filemode.Regular, Hash: added},
		object.TreeEntry{Name: "modified.txt", Mode: filemode.Regular, Hash: modified},
		object.TreeEntry{Name: "binary.dat", Mode: filemode.Regular, Hash: binaryAfter},
	)
	files, err := compareTrees(context.Background(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	byPath := make(map[string]repositoryComparisonFile, len(files))
	for _, file := range files {
		byPath[file.Path] = file
	}
	if byPath["added.txt"].Status != "added" ||
		byPath["deleted.txt"].Status != "deleted" {
		t.Fatalf("missing add/delete statuses: %#v", byPath)
	}
	modification := byPath["modified.txt"]
	if modification.Status != "modified" ||
		modification.Additions != 2 ||
		modification.Deletions != 1 ||
		!strings.Contains(modification.Patch, "changed") {
		t.Fatalf("unexpected text modification: %#v", modification)
	}
	if !byPath["binary.dat"].Binary || byPath["binary.dat"].Patch != "" {
		t.Fatalf("unexpected binary modification: %#v", byPath["binary.dat"])
	}
}

func TestMergeLineHelpers(t *testing.T) {
	for content, want := range map[string]int{
		"":       0,
		"one":    1,
		"one\n":  1,
		"a\nb\n": 2,
	} {
		if got := diffLineCount(content); got != want {
			t.Fatalf("diffLineCount(%q) = %d, want %d", content, got, want)
		}
	}
	if got := splitTextLines(""); got != nil {
		t.Fatalf("splitTextLines(empty) = %#v", got)
	}
	if got := splitTextLines("one\ntwo"); len(got) != 2 ||
		got[0] != "one\n" || got[1] != "two" {
		t.Fatalf("splitTextLines() = %#v", got)
	}

	base := lineEdit{start: 1, end: 2, replacement: []string{"changed\n"}}
	if !editsEqual(base, base) ||
		editsEqual(base, lineEdit{start: 0, end: 2, replacement: base.replacement}) ||
		editsEqual(base, lineEdit{start: 1, end: 2}) ||
		editsEqual(base, lineEdit{start: 1, end: 2, replacement: []string{"other\n"}}) {
		t.Fatal("edit equality produced an incorrect result")
	}
	for _, test := range []struct {
		left, right lineEdit
		want        bool
	}{
		{left: lineEdit{start: 1, end: 1}, right: lineEdit{start: 1, end: 1}, want: true},
		{left: lineEdit{start: 1, end: 1}, right: lineEdit{start: 0, end: 2}, want: true},
		{left: lineEdit{start: 0, end: 2}, right: lineEdit{start: 1, end: 1}, want: true},
		{left: lineEdit{start: 0, end: 2}, right: lineEdit{start: 1, end: 3}, want: true},
		{left: lineEdit{start: 0, end: 1}, right: lineEdit{start: 1, end: 2}, want: false},
	} {
		if got := editsOverlap(test.left, test.right); got != test.want {
			t.Fatalf("editsOverlap(%#v, %#v) = %v", test.left, test.right, got)
		}
	}
}
