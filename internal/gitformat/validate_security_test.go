package gitformat

import (
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
)

type nativeGraphFixture struct {
	blob   plumbing.Hash
	tree   plumbing.Hash
	commit plumbing.Hash
}

func TestValidateReachableRejectsMalformedNativeObjects(t *testing.T) {
	tests := []struct {
		name string
		want string
		root func(*testing.T, *git.Repository, nativeGraphFixture) plumbing.Hash
	}{
		{
			name: "tree missing mode",
			want: "missing a mode",
			root: rawNativeTestObject(plumbing.TreeObject, func(nativeGraphFixture) []byte {
				return []byte("README")
			}),
		},
		{
			name: "tree missing name terminator",
			want: "missing a name terminator",
			root: rawNativeTestObject(plumbing.TreeObject, func(nativeGraphFixture) []byte {
				return []byte("100644 README")
			}),
		},
		{
			name: "tree forbidden dot name",
			want: "forbidden name",
			root: rawNativeTestObject(plumbing.TreeObject, func(f nativeGraphFixture) []byte {
				return testTreeEntry("100644", "..", f.blob)
			}),
		},
		{
			name: "tree truncated id",
			want: "truncated object ID",
			root: rawNativeTestObject(plumbing.TreeObject, func(nativeGraphFixture) []byte {
				return append([]byte("100644 README\x00"), bytes.Repeat([]byte{1}, 31)...)
			}),
		},
		{
			name: "tree zero id",
			want: "invalid SHA-256 object ID",
			root: rawNativeTestObject(plumbing.TreeObject, func(nativeGraphFixture) []byte {
				return append([]byte("100644 README\x00"), make([]byte, 32)...)
			}),
		},
		{
			name: "tree unsupported mode",
			want: "unsupported mode",
			root: rawNativeTestObject(plumbing.TreeObject, func(f nativeGraphFixture) []byte {
				return testTreeEntry("040000", "directory", f.tree)
			}),
		},
		{
			name: "tree duplicate entry",
			want: "duplicate entry",
			root: rawNativeTestObject(plumbing.TreeObject, func(f nativeGraphFixture) []byte {
				return append(
					testTreeEntry("100644", "same", f.blob),
					testTreeEntry("100755", "same", f.blob)...,
				)
			}),
		},
		{
			name: "tree noncanonical order",
			want: "not in canonical order",
			root: rawNativeTestObject(plumbing.TreeObject, func(f nativeGraphFixture) []byte {
				return append(
					testTreeEntry("100644", "z", f.blob),
					testTreeEntry("100644", "a", f.blob)...,
				)
			}),
		},
		{
			name: "tree child type mismatch",
			want: "expected blob",
			root: rawNativeTestObject(plumbing.TreeObject, func(f nativeGraphFixture) []byte {
				return testTreeEntry("100644", "tree-as-file", f.tree)
			}),
		},
		{
			name: "commit missing terminator",
			want: "missing header terminator",
			root: rawNativeTestObject(plumbing.CommitObject, func(f nativeGraphFixture) []byte {
				return []byte(fmt.Sprintf("tree %s\nauthor %s\ncommitter %s", f.tree, testIdentity, testIdentity))
			}),
		},
		{
			name: "commit missing required headers",
			want: "missing required commit headers",
			root: rawNativeTestObject(plumbing.CommitObject, func(f nativeGraphFixture) []byte {
				return []byte(fmt.Sprintf("tree %s\n\nbroken\n", f.tree))
			}),
		},
		{
			name: "commit noncanonical tree header",
			want: "tree header must be in canonical position",
			root: rawNativeTestObject(plumbing.CommitObject, func(f nativeGraphFixture) []byte {
				return []byte(fmt.Sprintf("object %s\nauthor %s\ncommitter %s\n\nbroken\n", f.tree, testIdentity, testIdentity))
			}),
		},
		{
			name: "commit invalid tree id",
			want: "invalid SHA-256 object ID",
			root: rawNativeTestObject(plumbing.CommitObject, func(nativeGraphFixture) []byte {
				return []byte(fmt.Sprintf("tree bad\nauthor %s\ncommitter %s\n\nbroken\n", testIdentity, testIdentity))
			}),
		},
		{
			name: "commit parent type mismatch",
			want: "expected commit",
			root: rawNativeTestObject(plumbing.CommitObject, func(f nativeGraphFixture) []byte {
				return []byte(fmt.Sprintf(
					"tree %s\nparent %s\nauthor %s\ncommitter %s\n\nbroken\n",
					f.tree, f.tree, testIdentity, testIdentity,
				))
			}),
		},
		{
			name: "commit invalid parent id",
			want: "commit parent has invalid SHA-256 object ID",
			root: rawNativeTestObject(plumbing.CommitObject, func(f nativeGraphFixture) []byte {
				return []byte(fmt.Sprintf(
					"tree %s\nparent bad\nauthor %s\ncommitter %s\n\nbroken\n",
					f.tree, testIdentity, testIdentity,
				))
			}),
		},
		{
			name: "commit missing author",
			want: "missing author header",
			root: rawNativeTestObject(plumbing.CommitObject, func(f nativeGraphFixture) []byte {
				return []byte(fmt.Sprintf(
					"tree %s\nparent %s\nparent %s\n\nbroken\n",
					f.tree, f.commit, f.commit,
				))
			}),
		},
		{
			name: "commit malformed author",
			want: "malformed author header",
			root: rawNativeTestObject(plumbing.CommitObject, func(f nativeGraphFixture) []byte {
				return []byte(fmt.Sprintf("tree %s\nauthor \ncommitter %s\n\nbroken\n", f.tree, testIdentity))
			}),
		},
		{
			name: "commit missing committer",
			want: "missing committer header",
			root: rawNativeTestObject(plumbing.CommitObject, func(f nativeGraphFixture) []byte {
				return []byte(fmt.Sprintf(
					"tree %s\nparent %s\nauthor %s\n\nbroken\n",
					f.tree, f.commit, testIdentity,
				))
			}),
		},
		{
			name: "commit malformed committer",
			want: "malformed committer header",
			root: rawNativeTestObject(plumbing.CommitObject, func(f nativeGraphFixture) []byte {
				return []byte(fmt.Sprintf("tree %s\nauthor %s\ncommitter \n\nbroken\n", f.tree, testIdentity))
			}),
		},
		{
			name: "commit orphan continuation",
			want: "orphan commit header continuation",
			root: rawNativeTestObject(plumbing.CommitObject, func(f nativeGraphFixture) []byte {
				return []byte(fmt.Sprintf(
					"tree %s\nauthor %s\ncommitter %s\n continuation\n\nbroken\n",
					f.tree, testIdentity, testIdentity,
				))
			}),
		},
		{
			name: "commit duplicate canonical header",
			want: "duplicate or out-of-order commit header",
			root: rawNativeTestObject(plumbing.CommitObject, func(f nativeGraphFixture) []byte {
				return []byte(fmt.Sprintf(
					"tree %s\nauthor %s\ncommitter %s\ntree %s\n\nbroken\n",
					f.tree, testIdentity, testIdentity, f.tree,
				))
			}),
		},
		{
			name: "commit malformed custom header",
			want: "invalid header name",
			root: rawNativeTestObject(plumbing.CommitObject, func(f nativeGraphFixture) []byte {
				return []byte(fmt.Sprintf(
					"tree %s\nauthor %s\ncommitter %s\nBad value\n\nbroken\n",
					f.tree, testIdentity, testIdentity,
				))
			}),
		},
		{
			name: "commit duplicate encoding",
			want: "duplicate or empty commit encoding",
			root: rawNativeTestObject(plumbing.CommitObject, func(f nativeGraphFixture) []byte {
				return []byte(fmt.Sprintf(
					"tree %s\nauthor %s\ncommitter %s\nencoding UTF-8\nencoding latin1\n\nbroken\n",
					f.tree, testIdentity, testIdentity,
				))
			}),
		},
		{
			name: "commit encoding continuation",
			want: "encoding header has a continuation",
			root: rawNativeTestObject(plumbing.CommitObject, func(f nativeGraphFixture) []byte {
				return []byte(fmt.Sprintf(
					"tree %s\nauthor %s\ncommitter %s\nencoding UTF-8\n continued\n\nbroken\n",
					f.tree, testIdentity, testIdentity,
				))
			}),
		},
		{
			name: "commit malformed mergetag",
			want: "invalid mergetag",
			root: rawNativeTestObject(plumbing.CommitObject, func(f nativeGraphFixture) []byte {
				return []byte(fmt.Sprintf(
					"tree %s\nauthor %s\ncommitter %s\nmergetag invalid\n\nbroken\n",
					f.tree, testIdentity, testIdentity,
				))
			}),
		},
		{
			name: "tag missing required headers",
			want: "missing required tag headers",
			root: rawNativeTestObject(plumbing.TagObject, func(f nativeGraphFixture) []byte {
				return []byte(fmt.Sprintf("object %s\n\nbroken\n", f.commit))
			}),
		},
		{
			name: "tag empty header block",
			want: "empty header block",
			root: rawNativeTestObject(plumbing.TagObject, func(nativeGraphFixture) []byte {
				return []byte("\n\nbroken\n")
			}),
		},
		{
			name: "tag malformed header bytes",
			want: "malformed tag header",
			root: rawNativeTestObject(plumbing.TagObject, func(f nativeGraphFixture) []byte {
				return []byte(fmt.Sprintf("object %s\r\ntype commit\ntag v1\n\nbroken\n", f.commit))
			}),
		},
		{
			name: "tag noncanonical object header",
			want: "object header must be in canonical position",
			root: rawNativeTestObject(plumbing.TagObject, func(f nativeGraphFixture) []byte {
				return []byte(fmt.Sprintf("target %s\ntype commit\ntag v1\n\nbroken\n", f.commit))
			}),
		},
		{
			name: "tag invalid target id",
			want: "tag target has invalid SHA-256 object ID",
			root: rawNativeTestObject(plumbing.TagObject, func(nativeGraphFixture) []byte {
				return []byte("object bad\ntype commit\ntag v1\n\nbroken\n")
			}),
		},
		{
			name: "tag noncanonical type header",
			want: "type header must be in canonical position",
			root: rawNativeTestObject(plumbing.TagObject, func(f nativeGraphFixture) []byte {
				return []byte(fmt.Sprintf("object %s\ntarget commit\ntag v1\n\nbroken\n", f.commit))
			}),
		},
		{
			name: "tag invalid target type",
			want: "invalid tag target type",
			root: rawNativeTestObject(plumbing.TagObject, func(f nativeGraphFixture) []byte {
				return []byte(fmt.Sprintf("object %s\ntype ref-delta\ntag v1\n\nbroken\n", f.commit))
			}),
		},
		{
			name: "tag empty name",
			want: "malformed tag name",
			root: rawNativeTestObject(plumbing.TagObject, func(f nativeGraphFixture) []byte {
				return []byte(fmt.Sprintf("object %s\ntype commit\ntag \n\nbroken\n", f.commit))
			}),
		},
		{
			name: "tag malformed tagger",
			want: "malformed tagger header",
			root: rawNativeTestObject(plumbing.TagObject, func(f nativeGraphFixture) []byte {
				return []byte(fmt.Sprintf("object %s\ntype commit\ntag v1\ntagger \n\nbroken\n", f.commit))
			}),
		},
		{
			name: "tag orphan continuation",
			want: "orphan tag header continuation",
			root: rawNativeTestObject(plumbing.TagObject, func(f nativeGraphFixture) []byte {
				return []byte(fmt.Sprintf("object %s\ntype commit\ntag v1\n continuation\n\nbroken\n", f.commit))
			}),
		},
		{
			name: "tag duplicate canonical header",
			want: "duplicate or out-of-order tag header",
			root: rawNativeTestObject(plumbing.TagObject, func(f nativeGraphFixture) []byte {
				return []byte(fmt.Sprintf("object %s\ntype commit\ntag v1\ntag again\n\nbroken\n", f.commit))
			}),
		},
		{
			name: "tag malformed custom header",
			want: "invalid header name",
			root: rawNativeTestObject(plumbing.TagObject, func(f nativeGraphFixture) []byte {
				return []byte(fmt.Sprintf("object %s\ntype commit\ntag v1\nBad value\n\nbroken\n", f.commit))
			}),
		},
		{
			name: "tag target type mismatch",
			want: "expected tree",
			root: rawNativeTestObject(plumbing.TagObject, func(f nativeGraphFixture) []byte {
				return []byte(fmt.Sprintf("object %s\ntype tree\ntag v1\n\nbroken\n", f.commit))
			}),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo, fixture := newNativeGraphFixture(t)
			defer func() { _ = repo.Close() }()
			root := test.root(t, repo, fixture)
			setTestRef(t, repo, plumbing.NewHashReference("refs/heads/main", root))

			err := ValidateReachable(repo)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateReachable error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestValidateReachableRejectsUnsafeSymbolicAndReplaceReferences(t *testing.T) {
	for _, test := range []struct {
		name string
		refs func(plumbing.Hash) []*plumbing.Reference
		want string
	}{
		{
			name: "missing symbolic target",
			refs: func(plumbing.Hash) []*plumbing.Reference {
				return []*plumbing.Reference{
					plumbing.NewSymbolicReference("refs/aliases/current", "refs/heads/missing"),
				}
			},
			want: "missing target",
		},
		{
			name: "symbolic cycle",
			refs: func(plumbing.Hash) []*plumbing.Reference {
				return []*plumbing.Reference{
					plumbing.NewSymbolicReference("refs/aliases/a", "refs/aliases/b"),
					plumbing.NewSymbolicReference("refs/aliases/b", "refs/aliases/a"),
				}
			},
			want: "symbolic reference cycle",
		},
		{
			name: "replace ref short original",
			refs: func(blob plumbing.Hash) []*plumbing.Reference {
				return []*plumbing.Reference{
					plumbing.NewHashReference("refs/replace/deadbeef", blob),
				}
			},
			want: "does not name a full SHA-256 object",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo, fixture := newNativeGraphFixture(t)
			defer func() { _ = repo.Close() }()
			for _, ref := range test.refs(fixture.blob) {
				setTestRef(t, repo, ref)
			}
			err := ValidateReachable(repo)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateReachable error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestValidateReachableAllowsUnbornHEAD(t *testing.T) {
	repo, err := Init(filepath.Join(t.TempDir(), "repo.git"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repo.Close() }()
	setTestRef(t, repo, plumbing.NewSymbolicReference(plumbing.HEAD, "refs/heads/not-created"))
	if err := ValidateReachable(repo); err != nil {
		t.Fatalf("ValidateReachable: %v", err)
	}
}

func TestValidateReachableDetectsCorruptLooseObject(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "repo.git")
	repo, err := Init(repoPath, true)
	if err != nil {
		t.Fatal(err)
	}
	blob := storeTestObject(t, repo, plumbing.BlobObject, []byte("good"))
	setTestRef(t, repo, plumbing.NewHashReference("refs/heads/data", blob))
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}

	loosePath := filepath.Join(repoPath, "objects", blob.String()[:2], blob.String()[2:])
	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	if _, err := zw.Write([]byte("blob 4\x00evil")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(loosePath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(loosePath, compressed.Bytes(), 0o400); err != nil {
		t.Fatal(err)
	}

	repo, err = Open(repoPath)
	if err != nil {
		t.Fatalf("Open corrupt repository: %v", err)
	}
	defer func() { _ = repo.Close() }()
	err = ValidateReachable(repo)
	if err == nil || !strings.Contains(err.Error(), "object ID does not match content") {
		t.Fatalf("ValidateReachable error = %v, want content-address mismatch", err)
	}
}

func TestCompareTreeNamesUsesGitCanonicalOrdering(t *testing.T) {
	for _, test := range []struct {
		name       string
		a, b       string
		aDir, bDir bool
		wantSign   int
	}{
		{name: "different bytes", a: "a", b: "b", wantSign: -1},
		{name: "same files", a: "same", b: "same", wantSign: 0},
		{name: "file before its extension", a: "a", b: "a.c", wantSign: -1},
		{name: "directory after dot", a: "a", aDir: true, b: "a.c", wantSign: 1},
		{name: "longer left", a: "abc", b: "ab", wantSign: 1},
		{name: "directory suffixes equal name", a: "dir", aDir: true, b: "dir", bDir: true, wantSign: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := compareTreeNames([]byte(test.a), test.aDir, []byte(test.b), test.bDir)
			if got < 0 {
				got = -1
			} else if got > 0 {
				got = 1
			}
			if got != test.wantSign {
				t.Fatalf("compareTreeNames(%q, %v, %q, %v) sign = %d, want %d", test.a, test.aDir, test.b, test.bDir, got, test.wantSign)
			}
		})
	}
}

func newNativeGraphFixture(t *testing.T) (*git.Repository, nativeGraphFixture) {
	t.Helper()
	repo, err := Init(filepath.Join(t.TempDir(), "repo.git"), true)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	blob := storeTestObject(t, repo, plumbing.BlobObject, []byte("content"))
	tree := storeTestObject(t, repo, plumbing.TreeObject, nil)
	commit := testCommit(t, repo, tree, "valid parent")
	return repo, nativeGraphFixture{blob: blob, tree: tree, commit: commit}
}

func rawNativeTestObject(
	typ plumbing.ObjectType,
	body func(nativeGraphFixture) []byte,
) func(*testing.T, *git.Repository, nativeGraphFixture) plumbing.Hash {
	return func(t *testing.T, repo *git.Repository, fixture nativeGraphFixture) plumbing.Hash {
		t.Helper()
		return storeTestObject(t, repo, typ, body(fixture))
	}
}

func TestEqualBytesRejectsDifferentLengths(t *testing.T) {
	if equalBytes([]byte{1}, []byte{1, 0}) {
		t.Fatal("equalBytes accepted unequal lengths")
	}
}

func TestSHA256ObjectValidationRejectsDishonestStorageMetadata(t *testing.T) {
	body := []byte("structured body")
	validID := testSHA256ObjectID(plumbing.TreeObject, body)
	readFailure := errors.New("fixture read failure")
	closeFailure := errors.New("fixture close failure")
	readerFailure := errors.New("fixture reader failure")

	for _, test := range []struct {
		name string
		id   plumbing.Hash
		obj  plumbing.EncodedObject
		want string
	}{
		{
			name: "negative size",
			id:   validID,
			obj:  &adversarialEncodedObject{typ: plumbing.TreeObject, size: -1},
			want: "structured object size",
		},
		{
			name: "oversized",
			id:   validID,
			obj:  &adversarialEncodedObject{typ: plumbing.TreeObject, size: maxStructuredObjectSize + 1},
			want: "structured object size",
		},
		{
			name: "reader failure",
			id:   validID,
			obj: &adversarialEncodedObject{
				typ: plumbing.TreeObject, size: int64(len(body)), readerErr: readerFailure,
			},
			want: "fixture reader failure",
		},
		{
			name: "read failure",
			id:   validID,
			obj: &adversarialEncodedObject{
				typ: plumbing.TreeObject, size: int64(len(body)),
				reader: func() io.ReadCloser {
					return io.NopCloser(iotest.ErrReader(readFailure))
				},
			},
			want: "fixture read failure",
		},
		{
			name: "close failure",
			id:   validID,
			obj: &adversarialEncodedObject{
				typ: plumbing.TreeObject, size: int64(len(body)),
				reader: func() io.ReadCloser {
					return &adversarialReadCloser{Reader: bytes.NewReader(body), closeErr: closeFailure}
				},
			},
			want: "fixture close failure",
		},
		{
			name: "declared size mismatch",
			id:   validID,
			obj: &adversarialEncodedObject{
				typ: plumbing.TreeObject, size: int64(len(body) + 1),
				reader: func() io.ReadCloser {
					return io.NopCloser(bytes.NewReader(body))
				},
			},
			want: "declares size",
		},
		{
			name: "content address mismatch",
			id:   testSHA256ObjectID(plumbing.TreeObject, []byte("other")),
			obj: &adversarialEncodedObject{
				typ: plumbing.TreeObject, size: int64(len(body)),
				reader: func() io.ReadCloser {
					return io.NopCloser(bytes.NewReader(body))
				},
			},
			want: "object ID does not match content",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := readAndValidateSHA256Object(test.id, test.obj)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("readAndValidateSHA256Object error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestSHA256BlobValidationRejectsDishonestStorageMetadata(t *testing.T) {
	body := []byte("blob body")
	validID := testSHA256ObjectID(plumbing.BlobObject, body)
	for _, test := range []struct {
		name string
		id   plumbing.Hash
		obj  plumbing.EncodedObject
		want string
	}{
		{
			name: "negative size",
			id:   validID,
			obj:  &adversarialEncodedObject{typ: plumbing.BlobObject, size: -1},
			want: "negative blob size",
		},
		{
			name: "reader failure",
			id:   validID,
			obj: &adversarialEncodedObject{
				typ: plumbing.BlobObject, size: int64(len(body)), readerErr: errors.New("open reader"),
			},
			want: "open reader",
		},
		{
			name: "read failure",
			id:   validID,
			obj: &adversarialEncodedObject{
				typ: plumbing.BlobObject, size: int64(len(body)),
				reader: func() io.ReadCloser {
					return io.NopCloser(iotest.ErrReader(errors.New("read blob")))
				},
			},
			want: "read blob",
		},
		{
			name: "close failure",
			id:   validID,
			obj: &adversarialEncodedObject{
				typ: plumbing.BlobObject, size: int64(len(body)),
				reader: func() io.ReadCloser {
					return &adversarialReadCloser{
						Reader: bytes.NewReader(body), closeErr: errors.New("close blob"),
					}
				},
			},
			want: "close blob",
		},
		{
			name: "declared size mismatch",
			id:   validID,
			obj: &adversarialEncodedObject{
				typ: plumbing.BlobObject, size: int64(len(body) + 1),
				reader: func() io.ReadCloser {
					return io.NopCloser(bytes.NewReader(body))
				},
			},
			want: "declares size",
		},
		{
			name: "content address mismatch",
			id:   testSHA256ObjectID(plumbing.BlobObject, []byte("other")),
			obj: &adversarialEncodedObject{
				typ: plumbing.BlobObject, size: int64(len(body)),
				reader: func() io.ReadCloser {
					return io.NopCloser(bytes.NewReader(body))
				},
			},
			want: "object ID does not match content",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateSHA256Blob(test.id, test.obj)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateSHA256Blob error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestReachableValidatorRejectsInvalidCachedGraphState(t *testing.T) {
	id := testSHA256ObjectID(plumbing.BlobObject, []byte("content"))
	validator := &reachableValidator{
		done:     map[string]plumbing.ObjectType{id.String(): plumbing.BlobObject},
		visiting: make(map[string]bool),
	}
	if typ, err := validator.walk(id, plumbing.BlobObject); err != nil || typ != plumbing.BlobObject {
		t.Fatalf("cached walk = (%s, %v), want blob", typ, err)
	}
	if _, err := validator.walk(id, plumbing.TreeObject); err == nil || !strings.Contains(err.Error(), "expected tree") {
		t.Fatalf("cached type mismatch error = %v", err)
	}

	validator.done = make(map[string]plumbing.ObjectType)
	validator.visiting[id.String()] = true
	if _, err := validator.walk(id, plumbing.AnyObject); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle error = %v", err)
	}
	legacyID := plumbing.NewHash(strings.Repeat("1", 40))
	if _, err := validator.walk(legacyID, plumbing.AnyObject); err == nil || !strings.Contains(err.Error(), "invalid SHA-256") {
		t.Fatalf("legacy ID error = %v", err)
	}
}

type adversarialEncodedObject struct {
	typ       plumbing.ObjectType
	size      int64
	reader    func() io.ReadCloser
	readerErr error
}

func (o *adversarialEncodedObject) Hash() plumbing.Hash {
	return plumbing.ZeroHash
}

func (o *adversarialEncodedObject) Type() plumbing.ObjectType {
	return o.typ
}

func (o *adversarialEncodedObject) SetType(typ plumbing.ObjectType) {
	o.typ = typ
}

func (o *adversarialEncodedObject) Size() int64 {
	return o.size
}

func (o *adversarialEncodedObject) SetSize(size int64) {
	o.size = size
}

func (o *adversarialEncodedObject) Reader() (io.ReadCloser, error) {
	if o.readerErr != nil {
		return nil, o.readerErr
	}
	return o.reader(), nil
}

func (*adversarialEncodedObject) Writer() (io.WriteCloser, error) {
	return nil, errors.New("fixture does not support writing")
}

type adversarialReadCloser struct {
	io.Reader
	closeErr error
}

func (r *adversarialReadCloser) Close() error {
	return r.closeErr
}
