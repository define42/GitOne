package httpapi

import (
	"errors"
	"testing"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

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
