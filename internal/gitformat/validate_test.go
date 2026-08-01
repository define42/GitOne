package gitformat

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v6/plumbing"
)

func TestValidateReachableAllowsNativeSHA256Metadata(t *testing.T) {
	repo, err := Init(filepath.Join(t.TempDir(), "repo.git"), true)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer func() { _ = repo.Close() }()

	emptyTree := storeTestObject(t, repo, plumbing.TreeObject, nil)
	parent := testCommit(t, repo, emptyTree, "parent")
	externalSum := sha256.Sum256([]byte("external submodule commit"))
	external, ok := plumbing.FromBytes(externalSum[:])
	if !ok {
		t.Fatal("construct external gitlink ID")
	}
	gitlinkTree := storeTestObject(t, repo, plumbing.TreeObject,
		append([]byte("160000 dependency\x00"), external.Bytes()...))
	commitBody := []byte(fmt.Sprintf(
		"tree %s\nparent %s\nauthor %s\ncommitter %s\n"+
			"gpgsig -----BEGIN SSH SIGNATURE-----\n signature\n -----END SSH SIGNATURE-----\n"+
			"mergetag object %s\n type commit\n tag merged\n tagger %s\n \n merged tag\n -----BEGIN PGP SIGNATURE-----\n sig\n -----END PGP SIGNATURE-----\n"+
			"\nnative metadata\n",
		gitlinkTree, parent, testIdentity, testIdentity, parent, testIdentity,
	))
	commit := storeTestObject(t, repo, plumbing.CommitObject, commitBody)
	tagBody := []byte(fmt.Sprintf(
		"object %s\ntype commit\ntag signed-native\ntagger %s\n"+
			"gpgsig-sha256 -----BEGIN SSH SIGNATURE-----\n signature\n -----END SSH SIGNATURE-----\n\n"+
			"release\n-----BEGIN PGP SIGNATURE-----\nsig\n-----END PGP SIGNATURE-----\n",
		commit, testIdentity,
	))
	tag := storeTestObject(t, repo, plumbing.TagObject, tagBody)
	original := storeTestObject(t, repo, plumbing.BlobObject, []byte("original"))
	replacement := storeTestObject(t, repo, plumbing.BlobObject, []byte("replacement"))

	setTestRef(t, repo, plumbing.NewHashReference("refs/heads/main", commit))
	setTestRef(t, repo, plumbing.NewHashReference("refs/tags/signed-native", tag))
	setTestRef(t, repo, plumbing.NewHashReference("refs/notes/review", commit))
	setTestRef(t, repo, plumbing.NewHashReference(
		plumbing.ReferenceName("refs/replace/"+original.String()), replacement,
	))
	setTestRef(t, repo, plumbing.NewSymbolicReference(plumbing.HEAD, "refs/heads/main"))

	if err := ValidateReachable(repo); err != nil {
		t.Fatalf("ValidateReachable: %v", err)
	}
}

func TestValidateReachableRejectsMissingAndMismatchedObjects(t *testing.T) {
	for _, test := range []string{"missing tree", "wrong tree type"} {
		t.Run(test, func(t *testing.T) {
			repo, err := Init(filepath.Join(t.TempDir(), "repo.git"), true)
			if err != nil {
				t.Fatalf("Init: %v", err)
			}
			defer func() { _ = repo.Close() }()
			var tree plumbing.Hash
			if test == "missing tree" {
				missing := bytes.Repeat([]byte{0x42}, 32)
				tree, _ = plumbing.FromBytes(missing)
			} else {
				tree = storeTestObject(t, repo, plumbing.BlobObject, []byte("not a tree"))
			}
			commit := storeTestObject(t, repo, plumbing.CommitObject, []byte(fmt.Sprintf(
				"tree %s\nauthor %s\ncommitter %s\n\nbroken\n", tree, testIdentity, testIdentity,
			)))
			setTestRef(t, repo, plumbing.NewHashReference("refs/heads/main", commit))
			if err := ValidateReachable(repo); err == nil {
				t.Fatal("ValidateReachable succeeded, want error")
			}
		})
	}
}

func TestValidateReachableRejectsReplaceTypeChange(t *testing.T) {
	repo, err := Init(filepath.Join(t.TempDir(), "repo.git"), true)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer func() { _ = repo.Close() }()
	original := storeTestObject(t, repo, plumbing.BlobObject, []byte("original"))
	emptyTree := storeTestObject(t, repo, plumbing.TreeObject, nil)
	setTestRef(t, repo, plumbing.NewHashReference(
		plumbing.ReferenceName("refs/replace/"+original.String()), emptyTree,
	))
	err = ValidateReachable(repo)
	if err == nil || !strings.Contains(err.Error(), "changes object type") {
		t.Fatalf("ValidateReachable error = %v, want replace type error", err)
	}
}
