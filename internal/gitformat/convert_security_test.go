package gitformat

import (
	"bytes"
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

func TestConvertSHA1RepositoryRejectsMalformedObjectGraphs(t *testing.T) {
	requireLegacySHA1(t)

	tests := []struct {
		name string
		want string
		root func(*testing.T, *git.Repository, graphFixture) plumbing.Hash
	}{
		{
			name: "tree missing mode",
			want: "missing mode",
			root: rawTestObject(plumbing.TreeObject, func(graphFixture) []byte {
				return []byte("README")
			}),
		},
		{
			name: "tree missing name terminator",
			want: "missing name terminator",
			root: rawTestObject(plumbing.TreeObject, func(graphFixture) []byte {
				return []byte("100644 README")
			}),
		},
		{
			name: "tree empty name",
			want: "empty name",
			root: rawTestObject(plumbing.TreeObject, func(f graphFixture) []byte {
				return testTreeEntry("100644", "", f.blob)
			}),
		},
		{
			name: "tree slash in name",
			want: "name contains slash",
			root: rawTestObject(plumbing.TreeObject, func(f graphFixture) []byte {
				return testTreeEntry("100644", "a/b", f.blob)
			}),
		},
		{
			name: "tree dot git name",
			want: "forbidden .git name",
			root: rawTestObject(plumbing.TreeObject, func(f graphFixture) []byte {
				return testTreeEntry("100644", ".GiT", f.blob)
			}),
		},
		{
			name: "tree truncated object id",
			want: "truncated object ID",
			root: rawTestObject(plumbing.TreeObject, func(graphFixture) []byte {
				return append([]byte("100644 README\x00"), bytes.Repeat([]byte{1}, 19)...)
			}),
		},
		{
			name: "tree unsupported mode",
			want: "unsupported mode",
			root: rawTestObject(plumbing.TreeObject, func(f graphFixture) []byte {
				return testTreeEntry("100600", "README", f.blob)
			}),
		},
		{
			name: "tree duplicate entry",
			want: "duplicate entry",
			root: rawTestObject(plumbing.TreeObject, func(f graphFixture) []byte {
				return append(
					testTreeEntry("100644", "README", f.blob),
					testTreeEntry("100755", "README", f.blob)...,
				)
			}),
		},
		{
			name: "tree noncanonical order",
			want: "not in canonical order",
			root: rawTestObject(plumbing.TreeObject, func(f graphFixture) []byte {
				return append(
					testTreeEntry("100644", "z-last", f.blob),
					testTreeEntry("100644", "a-first", f.blob)...,
				)
			}),
		},
		{
			name: "tree target type mismatch",
			want: "expected blob",
			root: rawTestObject(plumbing.TreeObject, func(f graphFixture) []byte {
				return testTreeEntry("100644", "directory-as-file", f.tree)
			}),
		},
		{
			name: "commit missing terminator",
			want: "missing header terminator",
			root: rawTestObject(plumbing.CommitObject, func(f graphFixture) []byte {
				return []byte(fmt.Sprintf("tree %s\nauthor %s\ncommitter %s", f.tree, testIdentity, testIdentity))
			}),
		},
		{
			name: "commit empty header block",
			want: "empty header block",
			root: rawTestObject(plumbing.CommitObject, func(graphFixture) []byte {
				return []byte("\n\nbroken\n")
			}),
		},
		{
			name: "commit malformed header bytes",
			want: "malformed commit header",
			root: rawTestObject(plumbing.CommitObject, func(f graphFixture) []byte {
				return []byte(fmt.Sprintf("tree %s\nauthor %s\r\ncommitter %s\n\nbroken\n", f.tree, testIdentity, testIdentity))
			}),
		},
		{
			name: "commit missing required headers",
			want: "missing required headers",
			root: rawTestObject(plumbing.CommitObject, func(f graphFixture) []byte {
				return []byte(fmt.Sprintf("tree %s\n\nbroken\n", f.tree))
			}),
		},
		{
			name: "commit noncanonical tree header",
			want: "tree header must be in canonical position",
			root: rawTestObject(plumbing.CommitObject, func(f graphFixture) []byte {
				return []byte(fmt.Sprintf("object %s\nauthor %s\ncommitter %s\n\nbroken\n", f.tree, testIdentity, testIdentity))
			}),
		},
		{
			name: "commit invalid tree id",
			want: "invalid SHA-1 object ID",
			root: rawTestObject(plumbing.CommitObject, func(graphFixture) []byte {
				return []byte(fmt.Sprintf(
					"tree %s\nauthor %s\ncommitter %s\n\nbroken\n",
					strings.Repeat("G", 40), testIdentity, testIdentity,
				))
			}),
		},
		{
			name: "commit tree type mismatch",
			want: "expected tree",
			root: rawTestObject(plumbing.CommitObject, func(f graphFixture) []byte {
				return []byte(fmt.Sprintf(
					"tree %s\nauthor %s\ncommitter %s\n\nbroken\n",
					f.blob, testIdentity, testIdentity,
				))
			}),
		},
		{
			name: "commit parent type mismatch",
			want: "expected commit",
			root: rawTestObject(plumbing.CommitObject, func(f graphFixture) []byte {
				return []byte(fmt.Sprintf(
					"tree %s\nparent %s\nauthor %s\ncommitter %s\n\nbroken\n",
					f.tree, f.tree, testIdentity, testIdentity,
				))
			}),
		},
		{
			name: "commit invalid parent id",
			want: "commit parent has invalid SHA-1 object ID",
			root: rawTestObject(plumbing.CommitObject, func(f graphFixture) []byte {
				return []byte(fmt.Sprintf(
					"tree %s\nparent bad\nauthor %s\ncommitter %s\n\nbroken\n",
					f.tree, testIdentity, testIdentity,
				))
			}),
		},
		{
			name: "commit missing author",
			want: "missing author header",
			root: rawTestObject(plumbing.CommitObject, func(f graphFixture) []byte {
				return []byte(fmt.Sprintf(
					"tree %s\nparent %s\nparent %s\n\nbroken\n",
					f.tree, f.commit, f.commit,
				))
			}),
		},
		{
			name: "commit missing committer",
			want: "missing committer",
			root: rawTestObject(plumbing.CommitObject, func(f graphFixture) []byte {
				return []byte(fmt.Sprintf(
					"tree %s\nparent %s\nauthor %s\n\nbroken\n",
					f.tree, f.commit, testIdentity,
				))
			}),
		},
		{
			name: "commit empty author",
			want: "malformed commit",
			root: rawTestObject(plumbing.CommitObject, func(f graphFixture) []byte {
				return []byte(fmt.Sprintf("tree %s\nauthor \ncommitter %s\n\nbroken\n", f.tree, testIdentity))
			}),
		},
		{
			name: "commit duplicate encoding",
			want: "unsupported commit header",
			root: rawTestObject(plumbing.CommitObject, func(f graphFixture) []byte {
				return []byte(fmt.Sprintf(
					"tree %s\nauthor %s\ncommitter %s\nencoding UTF-8\nencoding latin1\n\nbroken\n",
					f.tree, testIdentity, testIdentity,
				))
			}),
		},
		{
			name: "commit invalid header name",
			want: "invalid header name",
			root: rawTestObject(plumbing.CommitObject, func(f graphFixture) []byte {
				return []byte(fmt.Sprintf(
					"tree %s\nauthor %s\ncommitter %s\nEncoding UTF-8\n\nbroken\n",
					f.tree, testIdentity, testIdentity,
				))
			}),
		},
		{
			name: "tag invalid target id",
			want: "invalid SHA-1 object ID",
			root: rawTestObject(plumbing.TagObject, func(graphFixture) []byte {
				return []byte("object bad\ntype commit\ntag v1\n\nrelease\n")
			}),
		},
		{
			name: "tag missing terminator",
			want: "missing header terminator",
			root: rawTestObject(plumbing.TagObject, func(f graphFixture) []byte {
				return []byte(fmt.Sprintf("object %s\ntype commit\ntag v1", f.commit))
			}),
		},
		{
			name: "tag missing required headers",
			want: "missing required headers",
			root: rawTestObject(plumbing.TagObject, func(f graphFixture) []byte {
				return []byte(fmt.Sprintf("object %s\n\nbroken\n", f.commit))
			}),
		},
		{
			name: "tag noncanonical object header",
			want: "object header must be in canonical position",
			root: rawTestObject(plumbing.TagObject, func(f graphFixture) []byte {
				return []byte(fmt.Sprintf("target %s\ntype commit\ntag v1\n\nbroken\n", f.commit))
			}),
		},
		{
			name: "tag noncanonical type header",
			want: "type header must be in canonical position",
			root: rawTestObject(plumbing.TagObject, func(f graphFixture) []byte {
				return []byte(fmt.Sprintf("object %s\ntarget commit\ntag v1\n\nbroken\n", f.commit))
			}),
		},
		{
			name: "tag invalid target type",
			want: "target type",
			root: rawTestObject(plumbing.TagObject, func(f graphFixture) []byte {
				return []byte(fmt.Sprintf("object %s\ntype ofs-delta\ntag v1\n\nrelease\n", f.commit))
			}),
		},
		{
			name: "tag target type mismatch",
			want: "expected commit",
			root: rawTestObject(plumbing.TagObject, func(f graphFixture) []byte {
				return []byte(fmt.Sprintf("object %s\ntype commit\ntag v1\n\nrelease\n", f.blob))
			}),
		},
		{
			name: "tag empty name",
			want: "malformed tag",
			root: rawTestObject(plumbing.TagObject, func(f graphFixture) []byte {
				return []byte(fmt.Sprintf("object %s\ntype commit\ntag \n\nrelease\n", f.commit))
			}),
		},
		{
			name: "tag unsupported header",
			want: "unsupported tag header",
			root: rawTestObject(plumbing.TagObject, func(f graphFixture) []byte {
				return []byte(fmt.Sprintf("object %s\ntype commit\ntag v1\nextra value\n\nrelease\n", f.commit))
			}),
		},
		{
			name: "tag malformed header",
			want: "malformed tag header",
			root: rawTestObject(plumbing.TagObject, func(f graphFixture) []byte {
				return []byte(fmt.Sprintf("object %s\ntype commit\ntag v1\ninvalid\n\nbroken\n", f.commit))
			}),
		},
		{
			name: "tag signature header",
			want: "signed tags are not supported",
			root: rawTestObject(plumbing.TagObject, func(f graphFixture) []byte {
				return []byte(fmt.Sprintf("object %s\ntype commit\ntag v1\ngpgsig signature\n\nbroken\n", f.commit))
			}),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sourcePath := filepath.Join(t.TempDir(), "source.git")
			source, err := git.PlainInit(sourcePath, true)
			if err != nil {
				t.Fatalf("PlainInit: %v", err)
			}
			blob := storeTestObject(t, source, plumbing.BlobObject, []byte("content"))
			tree := storeTestObject(t, source, plumbing.TreeObject, nil)
			commit := testCommit(t, source, tree, "valid parent")
			root := test.root(t, source, graphFixture{blob: blob, tree: tree, commit: commit})
			setTestRef(t, source, plumbing.NewHashReference("refs/heads/main", root))
			if err := source.Close(); err != nil {
				t.Fatalf("close source: %v", err)
			}

			destinationPath := filepath.Join(t.TempDir(), "destination.git")
			_, err = ConvertSHA1Repository(sourcePath, destinationPath)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ConvertSHA1Repository error = %v, want containing %q", err, test.want)
			}
			if _, statErr := os.Lstat(destinationPath); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("failed destination was not removed: %v", statErr)
			}
		})
	}
}

func TestConvertSHA1RepositorySupportsNonBareSource(t *testing.T) {
	requireLegacySHA1(t)
	sourcePath := filepath.Join(t.TempDir(), "source")
	source, err := git.PlainInit(sourcePath, false)
	if err != nil {
		t.Fatalf("PlainInit: %v", err)
	}
	tree := storeTestObject(t, source, plumbing.TreeObject, nil)
	commit := testCommit(t, source, tree, "non-bare")
	main := plumbing.NewBranchReferenceName("main")
	setTestRef(t, source, plumbing.NewHashReference(main, commit))
	setTestRef(t, source, plumbing.NewSymbolicReference(plumbing.HEAD, main))
	if err := source.Close(); err != nil {
		t.Fatalf("close source: %v", err)
	}

	destination, err := ConvertSHA1Repository(
		sourcePath,
		filepath.Join(t.TempDir(), "destination.git"),
	)
	if err != nil {
		t.Fatalf("ConvertSHA1Repository: %v", err)
	}
	defer func() { _ = destination.Close() }()
	if err := ValidateReachable(destination); err != nil {
		t.Fatalf("ValidateReachable: %v", err)
	}
}

func TestConvertSHA1RepositoryRewritesNestedTree(t *testing.T) {
	requireLegacySHA1(t)
	sourcePath := filepath.Join(t.TempDir(), "source.git")
	source, err := git.PlainInit(sourcePath, true)
	if err != nil {
		t.Fatal(err)
	}
	blob := storeTestObject(t, source, plumbing.BlobObject, []byte("nested"))
	childTree := storeTestObject(
		t,
		source,
		plumbing.TreeObject,
		testTreeEntry("100755", "script", blob),
	)
	rootTree := storeTestObject(
		t,
		source,
		plumbing.TreeObject,
		testTreeEntry("40000", "directory", childTree),
	)
	commit := testCommit(t, source, rootTree, "nested tree")
	setTestRef(t, source, plumbing.NewHashReference("refs/heads/main", commit))
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}

	destination, err := ConvertSHA1Repository(
		sourcePath,
		filepath.Join(t.TempDir(), "destination.git"),
	)
	if err != nil {
		t.Fatalf("ConvertSHA1Repository: %v", err)
	}
	defer func() { _ = destination.Close() }()
	if err := ValidateReachable(destination); err != nil {
		t.Fatalf("ValidateReachable: %v", err)
	}
}

func TestLegacyObjectReadersRejectDishonestStorageMetadata(t *testing.T) {
	requireLegacySHA1(t)
	legacyID := plumbing.NewHash(strings.Repeat("1", 40))
	body := []byte("body")
	for _, test := range []struct {
		name string
		obj  plumbing.EncodedObject
		want string
	}{
		{
			name: "negative structured size",
			obj:  &adversarialEncodedObject{typ: plumbing.TreeObject, size: -1},
			want: "structured object size",
		},
		{
			name: "oversized structured object",
			obj: &adversarialEncodedObject{
				typ: plumbing.TreeObject, size: maxStructuredObjectSize + 1,
			},
			want: "structured object size",
		},
		{
			name: "structured reader failure",
			obj: &adversarialEncodedObject{
				typ: plumbing.TreeObject, size: int64(len(body)), readerErr: errors.New("open legacy"),
			},
			want: "open legacy",
		},
		{
			name: "structured read failure",
			obj: &adversarialEncodedObject{
				typ: plumbing.TreeObject, size: int64(len(body)),
				reader: func() io.ReadCloser {
					return io.NopCloser(iotest.ErrReader(errors.New("read legacy")))
				},
			},
			want: "read legacy",
		},
		{
			name: "structured close failure",
			obj: &adversarialEncodedObject{
				typ: plumbing.TreeObject, size: int64(len(body)),
				reader: func() io.ReadCloser {
					return &adversarialReadCloser{
						Reader: bytes.NewReader(body), closeErr: errors.New("close legacy"),
					}
				},
			},
			want: "close legacy",
		},
		{
			name: "structured size mismatch",
			obj: &adversarialEncodedObject{
				typ: plumbing.TreeObject, size: int64(len(body) + 1),
				reader: func() io.ReadCloser {
					return io.NopCloser(bytes.NewReader(body))
				},
			},
			want: "declares size",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := readAndValidateSourceObject(legacyID, test.obj)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("readAndValidateSourceObject error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestLegacyBlobConversionRejectsDishonestStorageMetadata(t *testing.T) {
	requireLegacySHA1(t)
	target, err := Init(filepath.Join(t.TempDir(), "target.git"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = target.Close() }()
	converter := &objectConverter{target: target}
	legacyID := plumbing.NewHash(strings.Repeat("1", 40))
	body := []byte("body")
	for _, test := range []struct {
		name string
		obj  plumbing.EncodedObject
		want string
	}{
		{
			name: "negative size",
			obj:  &adversarialEncodedObject{typ: plumbing.BlobObject, size: -1},
			want: "negative blob size",
		},
		{
			name: "reader failure",
			obj: &adversarialEncodedObject{
				typ: plumbing.BlobObject, size: int64(len(body)), readerErr: errors.New("open blob"),
			},
			want: "open blob",
		},
		{
			name: "read failure",
			obj: &adversarialEncodedObject{
				typ: plumbing.BlobObject, size: int64(len(body)),
				reader: func() io.ReadCloser {
					return io.NopCloser(iotest.ErrReader(errors.New("read blob")))
				},
			},
			want: "read blob",
		},
		{
			name: "reader close failure",
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
			name: "source hash mismatch",
			obj: &adversarialEncodedObject{
				typ: plumbing.BlobObject, size: int64(len(body)),
				reader: func() io.ReadCloser {
					return io.NopCloser(bytes.NewReader(body))
				},
			},
			want: "source object hash mismatch",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := converter.convertBlob(legacyID, test.obj)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("convertBlob error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestRequireLegacySHA1(t *testing.T) {
	if err := RequireLegacySHA1(); err != nil {
		if errors.Is(err, ErrLegacySHA1Unavailable) {
			t.Skip("strict FIPS-only mode intentionally disables legacy conversion")
		}
		t.Fatalf("RequireLegacySHA1: %v", err)
	}
}

func TestConvertSHA1RepositoryRejectsUnsafeRepositoryState(t *testing.T) {
	requireLegacySHA1(t)

	for _, test := range []struct {
		name  string
		setup func(*testing.T) (string, string)
		want  string
	}{
		{
			name: "source is file",
			setup: func(t *testing.T) (string, string) {
				path := filepath.Join(t.TempDir(), "source.git")
				if err := os.WriteFile(path, []byte("not a repository"), 0o600); err != nil {
					t.Fatal(err)
				}
				return path, filepath.Join(t.TempDir(), "destination.git")
			},
			want: "not a directory",
		},
		{
			name: "linked worktree",
			setup: func(t *testing.T) (string, string) {
				path := t.TempDir()
				if err := os.WriteFile(filepath.Join(path, ".git"), []byte("gitdir: elsewhere\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return path, filepath.Join(t.TempDir(), "destination.git")
			},
			want: "linked worktrees",
		},
		{
			name: "config is directory",
			setup: func(t *testing.T) (string, string) {
				path := t.TempDir()
				if err := os.Mkdir(filepath.Join(path, "config"), 0o700); err != nil {
					t.Fatal(err)
				}
				return path, filepath.Join(t.TempDir(), "destination.git")
			},
			want: "config is a directory",
		},
		{
			name: "source is SHA-256",
			setup: func(t *testing.T) (string, string) {
				path := filepath.Join(t.TempDir(), "source.git")
				repo, err := Init(path, true)
				if err != nil {
					t.Fatal(err)
				}
				_ = repo.Close()
				return path, filepath.Join(t.TempDir(), "destination.git")
			},
			want: "want sha1",
		},
		{
			name: "promisor pack",
			setup: func(t *testing.T) (string, string) {
				path := newSHA1TestRepository(t)
				promisor := filepath.Join(path, "objects", "pack", "pack-fixture.promisor")
				if err := os.WriteFile(promisor, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				return path, filepath.Join(t.TempDir(), "destination.git")
			},
			want: "promisor objects",
		},
		{
			name: "partial clone extension",
			setup: func(t *testing.T) (string, string) {
				path := newSHA1TestRepository(t)
				configPath := filepath.Join(path, "config")
				config, err := os.ReadFile(configPath)
				if err != nil {
					t.Fatal(err)
				}
				config = append(config, []byte("[extensions]\n\tpartialClone = origin\n")...)
				if err := os.WriteFile(configPath, config, 0o600); err != nil {
					t.Fatal(err)
				}
				return path, filepath.Join(t.TempDir(), "destination.git")
			},
			want: "unknown extension: partialclone",
		},
		{
			name: "destination exists",
			setup: func(t *testing.T) (string, string) {
				destination := filepath.Join(t.TempDir(), "destination.git")
				if err := os.Mkdir(destination, 0o700); err != nil {
					t.Fatal(err)
				}
				return newSHA1TestRepository(t), destination
			},
			want: "destination already exists",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			source, destination := test.setup(t)
			_, err := ConvertSHA1Repository(source, destination)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ConvertSHA1Repository error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestConvertSHA1RepositoryRejectsUnsafeSymbolicReferences(t *testing.T) {
	requireLegacySHA1(t)

	for _, test := range []struct {
		name string
		refs []*plumbing.Reference
		want string
	}{
		{
			name: "missing non-head target",
			refs: []*plumbing.Reference{
				plumbing.NewSymbolicReference("refs/aliases/current", "refs/heads/missing"),
			},
			want: "missing target",
		},
		{
			name: "cycle",
			refs: []*plumbing.Reference{
				plumbing.NewSymbolicReference("refs/aliases/a", "refs/aliases/b"),
				plumbing.NewSymbolicReference("refs/aliases/b", "refs/aliases/a"),
			},
			want: "symbolic reference cycle",
		},
		{
			name: "notes target",
			refs: []*plumbing.Reference{
				plumbing.NewSymbolicReference("refs/aliases/notes", "refs/notes/review"),
			},
			want: "unsupported ref namespace",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			sourcePath := newSHA1TestRepository(t)
			source, err := git.PlainOpen(sourcePath)
			if err != nil {
				t.Fatal(err)
			}
			for _, ref := range test.refs {
				setTestRef(t, source, ref)
			}
			_ = source.Close()

			_, err = ConvertSHA1Repository(sourcePath, filepath.Join(t.TempDir(), "destination.git"))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ConvertSHA1Repository error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func rawTestObject(
	typ plumbing.ObjectType,
	body func(graphFixture) []byte,
) func(*testing.T, *git.Repository, graphFixture) plumbing.Hash {
	return func(t *testing.T, repo *git.Repository, fixture graphFixture) plumbing.Hash {
		t.Helper()
		return storeTestObject(t, repo, typ, body(fixture))
	}
}

type graphFixture struct {
	blob   plumbing.Hash
	tree   plumbing.Hash
	commit plumbing.Hash
}

func testTreeEntry(mode, name string, id plumbing.Hash) []byte {
	entry := []byte(mode + " " + name + "\x00")
	return append(entry, id.Bytes()...)
}

func newSHA1TestRepository(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.git")
	repo, err := git.PlainInit(path, true)
	if err != nil {
		t.Fatalf("PlainInit: %v", err)
	}
	if err := repo.Close(); err != nil {
		t.Fatalf("close source: %v", err)
	}
	return path
}
