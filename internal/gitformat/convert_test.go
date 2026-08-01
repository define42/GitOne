package gitformat

import (
	"bytes"
	"compress/zlib"
	"crypto"
	"crypto/sha1" // #nosec G505 -- verifies the registered legacy import implementation.
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	plumbinghash "github.com/go-git/go-git/v6/plumbing/hash"
)

const testIdentity = "A U Thor <author@example.com> 1700000000 +0000"

func TestConvertSHA1Repository(t *testing.T) {
	requireLegacySHA1(t)
	sourcePath := filepath.Join(t.TempDir(), "source.git")
	source, err := git.PlainInit(sourcePath, true)
	if err != nil {
		t.Fatalf("PlainInit source: %v", err)
	}

	blobBody := []byte("hello, SHA-256\n")
	blob := storeTestObject(t, source, plumbing.BlobObject, blobBody)
	treeBody := append([]byte("100644 README.md\x00"), blob.Bytes()...)
	tree := storeTestObject(t, source, plumbing.TreeObject, treeBody)
	commitOneBody := []byte(fmt.Sprintf(
		"tree %s\nauthor %s\ncommitter %s\n\nfirst\n", tree, testIdentity, testIdentity,
	))
	commitOne := storeTestObject(t, source, plumbing.CommitObject, commitOneBody)
	commitTwoBody := []byte(fmt.Sprintf(
		"tree %s\nparent %s\nauthor %s\ncommitter %s\nencoding UTF-8\n\nsecond mentions legacy %s\n",
		tree, commitOne, testIdentity, testIdentity, blob,
	))
	commitTwo := storeTestObject(t, source, plumbing.CommitObject, commitTwoBody)
	tagBody := []byte(fmt.Sprintf(
		"object %s\ntype commit\ntag v1.0.0\ntagger %s\n\nrelease\n", commitTwo, testIdentity,
	))
	tag := storeTestObject(t, source, plumbing.TagObject, tagBody)
	setTestRef(t, source, plumbing.NewHashReference("refs/heads/main", commitTwo))
	setTestRef(t, source, plumbing.NewHashReference("refs/tags/v1.0.0", tag))
	setTestRef(t, source, plumbing.NewSymbolicReference(plumbing.HEAD, "refs/heads/main"))
	_ = source.Close()

	destinationPath := filepath.Join(t.TempDir(), "destination.git")
	destination, err := ConvertSHA1Repository(sourcePath, destinationPath)
	if err != nil {
		t.Fatalf("ConvertSHA1Repository: %v", err)
	}
	defer func() { _ = destination.Close() }()
	if err := ValidateReachable(destination); err != nil {
		t.Fatalf("ValidateReachable: %v", err)
	}

	newBlob := testSHA256ObjectID(plumbing.BlobObject, blobBody)
	newTreeBody := append([]byte("100644 README.md\x00"), newBlob.Bytes()...)
	newTree := testSHA256ObjectID(plumbing.TreeObject, newTreeBody)
	newCommitOneBody := []byte(fmt.Sprintf(
		"tree %s\nauthor %s\ncommitter %s\n\nfirst\n", newTree, testIdentity, testIdentity,
	))
	newCommitOne := testSHA256ObjectID(plumbing.CommitObject, newCommitOneBody)
	newCommitTwoBody := []byte(fmt.Sprintf(
		"tree %s\nparent %s\nauthor %s\ncommitter %s\nencoding UTF-8\n\nsecond mentions legacy %s\n",
		newTree, newCommitOne, testIdentity, testIdentity, blob,
	))
	newCommitTwo := testSHA256ObjectID(plumbing.CommitObject, newCommitTwoBody)
	newTagBody := []byte(fmt.Sprintf(
		"object %s\ntype commit\ntag v1.0.0\ntagger %s\n\nrelease\n", newCommitTwo, testIdentity,
	))
	newTag := testSHA256ObjectID(plumbing.TagObject, newTagBody)

	assertTestRef(t, destination, "refs/heads/main", newCommitTwo)
	assertTestRef(t, destination, "refs/tags/v1.0.0", newTag)
	head, err := destination.Reference(plumbing.HEAD, false)
	if err != nil {
		t.Fatalf("read HEAD: %v", err)
	}
	if head.Type() != plumbing.SymbolicReference || head.Target() != "refs/heads/main" {
		t.Fatalf("HEAD = %s, want symbolic refs/heads/main", head)
	}
	for _, object := range []struct {
		typ  plumbing.ObjectType
		hash plumbing.Hash
		body []byte
	}{
		{plumbing.BlobObject, newBlob, blobBody},
		{plumbing.TreeObject, newTree, newTreeBody},
		{plumbing.CommitObject, newCommitOne, newCommitOneBody},
		{plumbing.CommitObject, newCommitTwo, newCommitTwoBody},
		{plumbing.TagObject, newTag, newTagBody},
	} {
		if got := readTestObject(t, destination, object.typ, object.hash); !bytes.Equal(got, object.body) {
			t.Errorf("%s %s body differs\ngot:  %q\nwant: %q", object.typ, object.hash, got, object.body)
		}
	}
}

func TestConvertSHA1RepositoryRejectsUnsupportedGraphsAndCleansDestination(t *testing.T) {
	requireLegacySHA1(t)
	for _, test := range []string{
		"missing object", "gitlink", "notes", "replace", "signed commit", "mergetag", "signed tag",
	} {
		t.Run(test, func(t *testing.T) {
			sourcePath := filepath.Join(t.TempDir(), "source.git")
			source, err := git.PlainInit(sourcePath, true)
			if err != nil {
				t.Fatalf("PlainInit: %v", err)
			}
			emptyTree := storeTestObject(t, source, plumbing.TreeObject, nil)
			var tip plumbing.Hash
			switch test {
			case "missing object":
				tip = storeTestObject(t, source, plumbing.CommitObject, []byte(fmt.Sprintf(
					"tree %s\nauthor %s\ncommitter %s\n\nbroken\n",
					strings.Repeat("1", 40), testIdentity, testIdentity,
				)))
			case "gitlink":
				gitlinkTree := storeTestObject(t, source, plumbing.TreeObject,
					append([]byte("160000 dependency\x00"), bytes.Repeat([]byte{1}, 20)...))
				tip = testCommit(t, source, gitlinkTree, "gitlink")
			case "signed commit":
				tip = storeTestObject(t, source, plumbing.CommitObject, []byte(fmt.Sprintf(
					"tree %s\nauthor %s\ncommitter %s\ngpgsig -----BEGIN PGP SIGNATURE-----\n abc\n -----END PGP SIGNATURE-----\n\nsigned\n",
					emptyTree, testIdentity, testIdentity,
				)))
			case "mergetag":
				tip = storeTestObject(t, source, plumbing.CommitObject, []byte(fmt.Sprintf(
					"tree %s\nauthor %s\ncommitter %s\nmergetag object %s\n type commit\n tag merged\n\nmerge\n",
					emptyTree, testIdentity, testIdentity, strings.Repeat("2", 40),
				)))
			case "signed tag":
				commit := testCommit(t, source, emptyTree, "tag target")
				tip = storeTestObject(t, source, plumbing.TagObject, []byte(fmt.Sprintf(
					"object %s\ntype commit\ntag signed\n\nmessage\n-----BEGIN SSH SIGNATURE-----\nsig\n", commit,
				)))
			default:
				tip = testCommit(t, source, emptyTree, test)
			}
			setTestRef(t, source, plumbing.NewHashReference("refs/heads/main", tip))
			if test == "notes" {
				setTestRef(t, source, plumbing.NewHashReference("refs/notes/test", tip))
			}
			if test == "replace" {
				setTestRef(t, source, plumbing.NewHashReference(
					plumbing.ReferenceName("refs/replace/"+tip.String()), tip,
				))
			}
			_ = source.Close()

			destinationPath := filepath.Join(t.TempDir(), "destination.git")
			if _, err := ConvertSHA1Repository(sourcePath, destinationPath); err == nil {
				t.Fatal("ConvertSHA1Repository succeeded, want rejection")
			}
			if _, err := os.Lstat(destinationPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed destination was not removed: %v", err)
			}
		})
	}
}

func TestConvertSHA1RepositoryValidatesSourceObjectHash(t *testing.T) {
	requireLegacySHA1(t)
	sourcePath := filepath.Join(t.TempDir(), "source.git")
	source, err := git.PlainInit(sourcePath, true)
	if err != nil {
		t.Fatalf("PlainInit: %v", err)
	}
	blob := storeTestObject(t, source, plumbing.BlobObject, []byte("good"))
	setTestRef(t, source, plumbing.NewHashReference("refs/heads/data", blob))
	_ = source.Close()

	loosePath := filepath.Join(sourcePath, "objects", blob.String()[:2], blob.String()[2:])
	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	_, _ = zw.Write([]byte("blob 4\x00evil"))
	if err := zw.Close(); err != nil {
		t.Fatalf("close zlib fixture: %v", err)
	}
	if err := os.Chmod(loosePath, 0o600); err != nil {
		t.Fatalf("make loose object writable: %v", err)
	}
	if err := os.WriteFile(loosePath, compressed.Bytes(), 0o444); err != nil {
		t.Fatalf("corrupt loose object: %v", err)
	}

	destinationPath := filepath.Join(t.TempDir(), "destination.git")
	_, err = ConvertSHA1Repository(sourcePath, destinationPath)
	if err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("ConvertSHA1Repository error = %v, want source hash mismatch", err)
	}
	if _, err := os.Lstat(destinationPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed destination was not removed: %v", err)
	}
}

func TestConvertSHA1RepositoryRejectsRepositoryDependencies(t *testing.T) {
	requireLegacySHA1(t)
	for _, relative := range []string{
		"shallow", "info/grafts", "objects/info/alternates", "objects/info/http-alternates",
	} {
		t.Run(relative, func(t *testing.T) {
			sourcePath := filepath.Join(t.TempDir(), "source.git")
			source, err := git.PlainInit(sourcePath, true)
			if err != nil {
				t.Fatalf("PlainInit: %v", err)
			}
			_ = source.Close()
			path := filepath.Join(sourcePath, filepath.FromSlash(relative))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}
			if err := os.WriteFile(path, []byte("fixture\n"), 0o600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			destinationPath := filepath.Join(t.TempDir(), "destination.git")
			if _, err := ConvertSHA1Repository(sourcePath, destinationPath); err == nil {
				t.Fatal("ConvertSHA1Repository succeeded, want rejection")
			}
		})
	}
}

func TestLegacySHA1StrictFIPSGuard(t *testing.T) {
	err := ensureLegacySHA1Available()
	if err == nil {
		t.Skip("strict FIPS-only mode is not active")
	}
	if !errors.Is(err, ErrLegacySHA1Unavailable) {
		t.Fatalf("guard error = %v, want ErrLegacySHA1Unavailable", err)
	}

	sourcePath := filepath.Join(t.TempDir(), "source.git")
	writeRawRepositoryConfig(t, sourcePath, "[core]\n\tbare = true\n")
	_, err = ConvertSHA1Repository(sourcePath, filepath.Join(t.TempDir(), "destination.git"))
	if !errors.Is(err, ErrLegacySHA1Unavailable) {
		t.Fatalf("ConvertSHA1Repository error = %v, want pre-open ErrLegacySHA1Unavailable", err)
	}
}

func TestGoGitSHA1UsesStandardLibrary(t *testing.T) {
	wantType := reflect.TypeOf(sha1.New()) // #nosec G401 -- test of the legacy import registration.
	gotType := reflect.TypeOf(plumbinghash.New(crypto.SHA1))
	if gotType != wantType {
		t.Fatalf("go-git SHA-1 implementation type = %v, want standard library %v", gotType, wantType)
	}
}

func requireLegacySHA1(t *testing.T) {
	t.Helper()
	if err := ensureLegacySHA1Available(); err != nil {
		if errors.Is(err, ErrLegacySHA1Unavailable) {
			t.Skip("strict FIPS-only mode intentionally disables legacy conversion")
		}
		t.Fatalf("SHA-1 availability: %v", err)
	}
}

func storeTestObject(t *testing.T, repo *git.Repository, typ plumbing.ObjectType, body []byte) plumbing.Hash {
	t.Helper()
	obj := repo.Storer.NewEncodedObject()
	obj.SetType(typ)
	obj.SetSize(int64(len(body)))
	w, err := obj.Writer()
	if err != nil {
		t.Fatalf("object writer: %v", err)
	}
	if _, err := w.Write(body); err != nil {
		t.Fatalf("write object: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close object: %v", err)
	}
	h, err := repo.Storer.SetEncodedObject(obj)
	if err != nil {
		t.Fatalf("store object: %v", err)
	}
	if h.IsZero() {
		t.Fatal("stored object has zero ID")
	}
	return h
}

func setTestRef(t *testing.T, repo *git.Repository, ref *plumbing.Reference) {
	t.Helper()
	if err := repo.Storer.SetReference(ref); err != nil {
		t.Fatalf("SetReference(%s): %v", ref.Name(), err)
	}
}

func testCommit(t *testing.T, repo *git.Repository, tree plumbing.Hash, message string) plumbing.Hash {
	t.Helper()
	return storeTestObject(t, repo, plumbing.CommitObject, []byte(fmt.Sprintf(
		"tree %s\nauthor %s\ncommitter %s\n\n%s\n", tree, testIdentity, testIdentity, message,
	)))
}

func testSHA256ObjectID(typ plumbing.ObjectType, body []byte) plumbing.Hash {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s %d\x00", typ, len(body))
	_, _ = h.Write(body)
	id, _ := plumbing.FromBytes(h.Sum(nil))
	return id
}

func readTestObject(t *testing.T, repo *git.Repository, typ plumbing.ObjectType, id plumbing.Hash) []byte {
	t.Helper()
	obj, err := repo.Storer.EncodedObject(typ, id)
	if err != nil {
		t.Fatalf("EncodedObject(%s): %v", id, err)
	}
	r, err := obj.Reader()
	if err != nil {
		t.Fatalf("Reader(%s): %v", id, err)
	}
	body, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll(%s): %v", id, err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close(%s): %v", id, err)
	}
	return body
}

func assertTestRef(t *testing.T, repo *git.Repository, name plumbing.ReferenceName, want plumbing.Hash) {
	t.Helper()
	ref, err := repo.Reference(name, false)
	if err != nil {
		t.Fatalf("Reference(%s): %v", name, err)
	}
	if !ref.Hash().Equal(want) {
		t.Fatalf("Reference(%s) = %s, want %s", name, ref.Hash(), want)
	}
}
