package githttp

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/define42/GitOne/internal/gitformat"
	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/storage"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/plumbing/protocol"
	"github.com/go-git/go-git/v6/plumbing/protocol/capability"
	"github.com/go-git/go-git/v6/plumbing/protocol/packp"
	"github.com/go-git/go-git/v6/plumbing/storer"
	gitstorage "github.com/go-git/go-git/v6/storage"
)

func TestReceiveAdvertisementProtocolVersions(t *testing.T) {
	handler := newSHA256SmartHTTPHandler(t, false)

	for _, test := range []struct {
		name           string
		gitProtocol    string
		wantSmartReply bool
		wantAdvVersion protocol.Version
	}{
		{
			name:           "version one",
			gitProtocol:    "version=1",
			wantSmartReply: true,
			wantAdvVersion: protocol.V1,
		},
		{
			name:           "version two falls back to version zero",
			gitProtocol:    "version=2",
			wantAdvVersion: protocol.V0,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(
				http.MethodGet,
				"/engineering/docs.git/info/refs?service=git-receive-pack",
				nil,
			)
			request.Header.Set("Git-Protocol", test.gitProtocol)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("advertisement returned %d: %s", response.Code, response.Body.String())
			}

			reader := bytes.NewReader(response.Body.Bytes())
			if test.wantSmartReply {
				var reply packp.SmartReply
				if err := reply.Decode(reader); err != nil {
					t.Fatalf("decode smart reply: %v", err)
				}
				if reply.Service != "git-receive-pack" {
					t.Fatalf("service = %q, want git-receive-pack", reply.Service)
				}
			} else if strings.Contains(response.Body.String(), "# service=git-receive-pack") {
				t.Fatal("protocol-v2 receive fallback included a smart HTTP service prefix")
			}

			var advertisement packp.AdvRefs
			if err := advertisement.Decode(reader); err != nil {
				t.Fatalf("decode advertisement: %v", err)
			}
			if advertisement.Version != test.wantAdvVersion {
				t.Fatalf("advertisement version = %d, want %d", advertisement.Version, test.wantAdvVersion)
			}
			formats := advertisement.Capabilities.Get(capability.ObjectFormat)
			if len(formats) != 1 || formats[0] != gitSHA256ObjectFormat {
				t.Fatalf("object-format = %v, want [sha256]", formats)
			}
		})
	}
}

func TestReceiveAdvertisementEncodesHeadPeeledTagsAndShallows(t *testing.T) {
	head := mustObjectID(t, strings.Repeat("a", 64))
	branch := mustObjectID(t, strings.Repeat("b", 64))
	tag := mustObjectID(t, strings.Repeat("c", 64))
	peeled := mustObjectID(t, strings.Repeat("d", 64))
	shallow := mustObjectID(t, strings.Repeat("e", 64))
	tagName := plumbing.NewTagReferenceName("v1.0.0")
	advertisement := &packp.AdvRefs{
		References: []*plumbing.Reference{
			plumbing.NewHashReference(plumbing.NewBranchReferenceName("z-last"), branch),
			plumbing.NewHashReference(tagName, tag),
			plumbing.NewHashReference(plumbing.ReferenceName(tagName.String()+"^{}"), peeled),
			plumbing.NewHashReference(plumbing.HEAD, head),
		},
		Shallows: []plumbing.Hash{shallow},
	}
	advertisement.Capabilities.Set(capability.ObjectFormat, gitSHA256ObjectFormat)

	var wire bytes.Buffer
	if err := encodeSHA256ReceiveAdvertisement(&wire, advertisement); err != nil {
		t.Fatal(err)
	}
	var decoded packp.AdvRefs
	if err := decoded.Decode(&wire); err != nil {
		t.Fatal(err)
	}
	if len(decoded.References) != 4 || decoded.References[0].Name() != plumbing.HEAD {
		t.Fatalf("reference order = %#v", decoded.References)
	}
	if len(decoded.Shallows) != 1 || decoded.Shallows[0] != shallow {
		t.Fatalf("shallows = %v, want [%s]", decoded.Shallows, shallow)
	}
	foundPeeled := false
	for _, reference := range decoded.References {
		if reference.Name() == plumbing.ReferenceName(tagName.String()+"^{}") {
			foundPeeled = reference.Hash() == peeled
		}
	}
	if !foundPeeled {
		t.Fatal("annotated tag peel was not preserved")
	}
}

func TestReceiveAdvertisementPropagatesBoundaryFailures(t *testing.T) {
	repository, err := gitformat.Init(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repository.Close() }()

	storageFailure := errors.New("reference enumeration failed")
	store := &advertisementReferenceStorer{
		Storer:  repository.Storer,
		iterErr: storageFailure,
	}
	if err = advertiseSHA256ReceivePack(store, "", &bytes.Buffer{}); !errors.Is(err, storageFailure) {
		t.Fatalf("reference storage failure returned %v", err)
	}

	writeFailure := errors.New("client disconnected")
	if err = advertiseSHA256ReceivePack(repository.Storer, "", errorWriter{writeFailure}); err == nil || !strings.Contains(err.Error(), "smart reply") {
		t.Fatalf("smart reply write failure returned %v", err)
	}

	if err = encodeSHA256ReceiveAdvertisement(&bytes.Buffer{}, &packp.AdvRefs{
		Version: protocol.V2,
	}); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported receive protocol version returned %v", err)
	}
	if err = encodeSHA256ReceiveAdvertisement(nil, &packp.AdvRefs{
		Version: protocol.V1,
	}); err == nil {
		t.Fatal("version-one advertisement ignored its writer failure")
	}
}

func TestReceiveAdvertisementPeelsAnnotatedTagWithSHA256IDs(t *testing.T) {
	root := t.TempDir()
	repositoryPath := filepath.Join(root, "engineering", "docs.git")
	if err := os.MkdirAll(filepath.Dir(repositoryPath), 0o750); err != nil {
		t.Fatal(err)
	}
	repository, err := gitformat.Init(repositoryPath, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repository.Close() }()

	blob := repository.Storer.NewEncodedObject()
	blob.SetType(plumbing.BlobObject)
	writer, err := blob.Writer()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Write([]byte("release artifact\n")); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	target, err := repository.Storer.SetEncodedObject(blob)
	if err != nil {
		t.Fatal(err)
	}

	encodedTag := repository.Storer.NewEncodedObject()
	tag := object.Tag{
		Name:       "v1.0.0",
		TargetType: plumbing.BlobObject,
		Target:     target,
		Message:    "release v1.0.0\n",
	}
	if err = tag.Encode(encodedTag); err != nil {
		t.Fatal(err)
	}
	tagHash, err := repository.Storer.SetEncodedObject(encodedTag)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = object.GetTag(repository.Storer, tagHash); err != nil {
		t.Fatalf("load annotated tag: %v", err)
	}
	tagName := plumbing.NewTagReferenceName(tag.Name)
	if err = repository.Storer.SetReference(plumbing.NewHashReference(tagName, tagHash)); err != nil {
		t.Fatal(err)
	}
	directAdvertisement := &packp.AdvRefs{}
	if err = addReceiveAdvertisementReferences(repository.Storer, directAdvertisement); err != nil {
		t.Fatalf("build direct advertisement: %v", err)
	}
	if len(directAdvertisement.References) != 2 {
		t.Fatalf("direct advertisement references = %#v", directAdvertisement.References)
	}

	response := httptest.NewRecorder()
	(Handler{Storage: storage.Store{Root: root}}).ServeHTTP(
		response,
		httptest.NewRequest(
			http.MethodGet,
			"/engineering/docs.git/info/refs?service=git-receive-pack",
			nil,
		),
	)
	if response.Code != http.StatusOK {
		t.Fatalf("advertisement returned %d: %s", response.Code, response.Body.String())
	}
	reader := bytes.NewReader(response.Body.Bytes())
	var reply packp.SmartReply
	if err = reply.Decode(reader); err != nil {
		t.Fatal(err)
	}
	var advertisement packp.AdvRefs
	if err = advertisement.Decode(reader); err != nil {
		t.Fatal(err)
	}

	want := map[plumbing.ReferenceName]plumbing.Hash{
		tagName: tagHash,
		plumbing.ReferenceName(tagName.String() + "^{}"): target,
	}
	for _, reference := range advertisement.References {
		if expected, ok := want[reference.Name()]; ok {
			if reference.Hash() != expected {
				t.Fatalf("%s = %s, want %s", reference.Name(), reference.Hash(), expected)
			}
			delete(want, reference.Name())
		}
	}
	if len(want) != 0 {
		t.Fatalf("advertisement omitted tag references: %v; got %#v", want, advertisement.References)
	}
}

func TestReceiveAdvertisementRejectsNonSHA256References(t *testing.T) {
	repository, err := gitformat.Init(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repository.Close() }()

	sha1ID := mustObjectID(t, strings.Repeat("1", 40))
	store := &advertisementReferenceStorer{
		Storer: repository.Storer,
		references: []*plumbing.Reference{
			plumbing.NewHashReference(plumbing.NewBranchReferenceName("legacy"), sha1ID),
		},
	}
	if err = addReceiveAdvertisementReferences(store, &packp.AdvRefs{}); err == nil ||
		!strings.Contains(err.Error(), "object ID must use SHA-256") {
		t.Fatalf("non-SHA-256 advertised reference returned %v", err)
	}

	encodedTag := repository.Storer.NewEncodedObject()
	malformedTag := object.Tag{
		Name:       "legacy-target",
		TargetType: plumbing.BlobObject,
		Target:     sha1ID,
		Message:    "must not be advertised\n",
	}
	if err = malformedTag.Encode(encodedTag); err != nil {
		t.Fatal(err)
	}
	tagHash, err := repository.Storer.SetEncodedObject(encodedTag)
	if err != nil {
		t.Fatal(err)
	}
	store.references = []*plumbing.Reference{
		plumbing.NewHashReference(plumbing.NewTagReferenceName(malformedTag.Name), tagHash),
	}
	if err = addReceiveAdvertisementReferences(store, &packp.AdvRefs{}); err == nil ||
		!strings.Contains(err.Error(), "peeled object ID must use SHA-256") {
		t.Fatalf("non-SHA-256 peeled tag target returned %v", err)
	}
}

func TestReceiveAdvertisementHandlesSymbolicReferencesSafely(t *testing.T) {
	repository, err := gitformat.Init(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repository.Close() }()

	main := plumbing.NewBranchReferenceName("main")
	alias := plumbing.ReferenceName("refs/aliases/current")
	sha256ID := mustObjectID(t, strings.Repeat("2", 64))
	if err = repository.Storer.SetReference(plumbing.NewHashReference(main, sha256ID)); err != nil {
		t.Fatal(err)
	}

	store := &advertisementReferenceStorer{
		Storer: repository.Storer,
		references: []*plumbing.Reference{
			plumbing.NewSymbolicReference(alias, main),
		},
	}
	advertisement := &packp.AdvRefs{}
	if err = addReceiveAdvertisementReferences(store, advertisement); err != nil {
		t.Fatal(err)
	}
	if len(advertisement.References) != 1 ||
		advertisement.References[0].Name() != alias ||
		advertisement.References[0].Hash() != sha256ID {
		t.Fatalf("resolved symbolic advertisement = %#v", advertisement.References)
	}

	store.references = []*plumbing.Reference{
		plumbing.NewSymbolicReference(alias, plumbing.NewBranchReferenceName("missing")),
	}
	advertisement = &packp.AdvRefs{}
	if err = addReceiveAdvertisementReferences(store, advertisement); err != nil {
		t.Fatalf("dangling symbolic reference: %v", err)
	}
	if len(advertisement.References) != 0 {
		t.Fatalf("dangling symbolic reference was advertised: %#v", advertisement.References)
	}

	store.referenceErr = errors.New("reference storage unavailable")
	if err = addReceiveAdvertisementReferences(store, &packp.AdvRefs{}); err == nil ||
		!strings.Contains(err.Error(), "reference storage unavailable") {
		t.Fatalf("symbolic reference storage failure returned %v", err)
	}
}

func TestReceiveValidationRejectsAmbiguousOrMissingInput(t *testing.T) {
	capabilities := &capability.List{}
	capabilities.Set(capability.ObjectFormat, gitSHA256ObjectFormat, gitSHA256ObjectFormat)
	if err := validateReceiveCapabilities(capabilities); err == nil ||
		!strings.Contains(err.Error(), "object-format must be sha256") {
		t.Fatalf("duplicate object-format values returned %v", err)
	}

	request := &packp.UpdateRequests{Commands: []*packp.Command{nil}}
	if err := validateReceiveObjectIDs(request); err == nil ||
		!strings.Contains(err.Error(), "command is missing") {
		t.Fatalf("missing receive command returned %v", err)
	}
}

func TestReceiveAdvertisementReportsWriteFailures(t *testing.T) {
	branch := plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("main"),
		mustObjectID(t, strings.Repeat("a", 64)),
	)
	tagName := plumbing.NewTagReferenceName("v1")
	tag := plumbing.NewHashReference(tagName, mustObjectID(t, strings.Repeat("b", 64)))
	peeled := plumbing.NewHashReference(
		plumbing.ReferenceName(tagName.String()+"^{}"),
		mustObjectID(t, strings.Repeat("c", 64)),
	)
	shallow := mustObjectID(t, strings.Repeat("d", 64))

	for _, test := range []struct {
		name       string
		writes     int
		references []*plumbing.Reference
		shallows   []plumbing.Hash
	}{
		{name: "empty first line", writes: 0},
		{name: "first reference", writes: 0, references: []*plumbing.Reference{branch}},
		{name: "second reference", writes: 2, references: []*plumbing.Reference{branch, tag}},
		{name: "peeled tag", writes: 2, references: []*plumbing.Reference{tag, peeled}},
		{name: "shallow boundary", writes: 2, references: []*plumbing.Reference{branch}, shallows: []plumbing.Hash{shallow}},
	} {
		t.Run(test.name, func(t *testing.T) {
			writer := &failAfterWriter{remaining: test.writes}
			err := encodeSHA256ReceiveAdvertisement(writer, &packp.AdvRefs{
				References: test.references,
				Shallows:   test.shallows,
			})
			if !errors.Is(err, errTestWriteFailure) {
				t.Fatalf("write failure returned %v", err)
			}
		})
	}
}

func TestReceivePackPushOptionsAndRepositoryBoundaries(t *testing.T) {
	makeRequest := func(t *testing.T, pushOptions *packp.PushOptions) *bytes.Buffer {
		t.Helper()
		update := &packp.UpdateRequests{Commands: []*packp.Command{{
			Name: plumbing.NewBranchReferenceName("main"),
			Old:  mustObjectID(t, strings.Repeat("3", 64)),
			New:  mustObjectID(t, strings.Repeat("4", 64)),
		}}}
		update.Capabilities.Set(capability.ObjectFormat, gitSHA256ObjectFormat)
		if pushOptions != nil {
			update.Capabilities.Set(capability.PushOptions)
		}
		var body bytes.Buffer
		if err := update.Encode(&body); err != nil {
			t.Fatal(err)
		}
		if pushOptions != nil {
			if err := pushOptions.Encode(&body); err != nil {
				t.Fatal(err)
			}
		}
		return &body
	}

	t.Run("malformed push options", func(t *testing.T) {
		body := makeRequest(t, nil)
		body.Reset()
		update := &packp.UpdateRequests{Commands: []*packp.Command{{
			Name: plumbing.NewBranchReferenceName("main"),
			Old:  mustObjectID(t, strings.Repeat("3", 64)),
			New:  mustObjectID(t, strings.Repeat("4", 64)),
		}}}
		update.Capabilities.Set(capability.ObjectFormat, gitSHA256ObjectFormat)
		update.Capabilities.Set(capability.PushOptions)
		if err := update.Encode(body); err != nil {
			t.Fatal(err)
		}
		_, _ = body.WriteString("not a packet line")

		response := httptest.NewRecorder()
		(Handler{Storage: storage.Store{Root: t.TempDir()}}).ServeHTTP(
			response,
			httptest.NewRequest(
				http.MethodPost,
				"/engineering/missing.git/git-receive-pack",
				body,
			),
		)
		if response.Code != http.StatusBadRequest ||
			!strings.Contains(response.Body.String(), "push options") {
			t.Fatalf("malformed push options returned %d: %s", response.Code, response.Body.String())
		}
	})

	t.Run("valid push options cannot hide a missing repository", func(t *testing.T) {
		body := makeRequest(t, &packp.PushOptions{Options: []string{"ci.skip"}})
		response := httptest.NewRecorder()
		(Handler{Storage: storage.Store{Root: t.TempDir()}}).ServeHTTP(
			response,
			httptest.NewRequest(
				http.MethodPost,
				"/engineering/missing.git/git-receive-pack",
				body,
			),
		)
		if response.Code != http.StatusNotFound {
			t.Fatalf("missing repository returned %d: %s", response.Code, response.Body.String())
		}
	})

	t.Run("control repository validation precedes access", func(t *testing.T) {
		update := &packp.UpdateRequests{Commands: []*packp.Command{{
			Name: plumbing.NewTagReferenceName("v1"),
			Old:  mustObjectID(t, strings.Repeat("3", 64)),
			New:  mustObjectID(t, strings.Repeat("4", 64)),
		}}}
		update.Capabilities.Set(capability.ObjectFormat, gitSHA256ObjectFormat)
		var body bytes.Buffer
		if err := update.Encode(&body); err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		(Handler{Storage: storage.Store{Root: t.TempDir()}}).ServeHTTP(
			response,
			httptest.NewRequest(
				http.MethodPost,
				"/engineering/control.git/git-receive-pack",
				&body,
			),
		)
		if response.Code != http.StatusConflict ||
			!strings.Contains(response.Body.String(), "refs/heads/main") {
			t.Fatalf("invalid control ref returned %d: %s", response.Code, response.Body.String())
		}
	})

	t.Run("authorization is rechecked under the operation lock", func(t *testing.T) {
		calls := 0
		handler := Handler{
			Storage: storage.Store{Root: t.TempDir()},
			Authorize: func(*http.Request, repopath.Repository, bool) (bool, bool) {
				calls++
				return true, calls == 1
			},
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(
			response,
			httptest.NewRequest(
				http.MethodPost,
				"/engineering/missing.git/git-receive-pack",
				makeRequest(t, nil),
			),
		)
		if response.Code != http.StatusForbidden || calls != 2 {
			t.Fatalf("locked authorization returned %d after %d checks", response.Code, calls)
		}
	})
}

func TestUploadPackMissingRepositoryReturnsNotFound(t *testing.T) {
	response := httptest.NewRecorder()
	(Handler{Storage: storage.Store{Root: t.TempDir()}}).ServeHTTP(
		response,
		httptest.NewRequest(
			http.MethodPost,
			"/engineering/missing.git/git-upload-pack",
			http.NoBody,
		),
	)
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing upload repository returned %d: %s", response.Code, response.Body.String())
	}
}

func TestReferenceDiscardClassificationFailsClosed(t *testing.T) {
	store := storage.Store{Root: t.TempDir()}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	repositoryPath := repopath.Repository{Groups: []string{"engineering"}, Name: "docs"}
	if err := store.CreateRepository(repositoryPath, storage.CreateRepositoryOptions{
		InitializeReadme: true,
		Author:           "alice",
	}); err != nil {
		t.Fatal(err)
	}
	gitPath, err := store.GitPath(repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := gitformat.Open(gitPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repository.Close() }()
	main := plumbing.NewBranchReferenceName("main")
	current, err := repository.Reference(main, false)
	if err != nil {
		t.Fatal(err)
	}
	missing := mustObjectID(t, strings.Repeat("f", 64))

	for _, test := range []struct {
		name    string
		command *packp.Command
	}{
		{
			name: "non-branch update",
			command: &packp.Command{
				Name: plumbing.NewTagReferenceName("v1"),
				Old:  current.Hash(),
				New:  missing,
			},
		},
		{
			name: "missing old commit",
			command: &packp.Command{
				Name: main,
				Old:  missing,
				New:  current.Hash(),
			},
		},
		{
			name: "missing new commit",
			command: &packp.Command{
				Name: main,
				Old:  current.Hash(),
				New:  missing,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if !referenceUpdateMayDiscardObjects(repository, test.command) {
				t.Fatal("uncertain update was treated as retaining all objects")
			}
		})
	}
}

func TestReceiveStatusSidebandAndNoReport(t *testing.T) {
	handler := Handler{}
	status := &packp.ReportStatus{UnpackStatus: "ok"}

	request := &packp.UpdateRequests{}
	response := httptest.NewRecorder()
	handler.writeReceiveStatus(response, request, status)
	if response.Body.Len() != 0 {
		t.Fatalf("status without report capability wrote %q", response.Body.String())
	}

	request.Capabilities.Set(capability.ReportStatus)
	request.Capabilities.Set(capability.Sideband)
	response = httptest.NewRecorder()
	handler.writeReceiveStatus(response, request, status)
	if !strings.Contains(response.Body.String(), "unpack ok") ||
		!strings.HasSuffix(response.Body.String(), "0000") {
		t.Fatalf("sideband status = %q", response.Body.String())
	}
}

func TestMalformedSHA256PackIsNotPublished(t *testing.T) {
	store := storage.Store{Root: t.TempDir()}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	repositoryPath := repopath.Repository{Groups: []string{"engineering"}, Name: "docs"}
	if err := store.CreateRepository(repositoryPath, storage.CreateRepositoryOptions{
		InitializeReadme: true,
		Author:           "alice",
	}); err != nil {
		t.Fatal(err)
	}
	gitPath, err := store.GitPath(repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := gitformat.Open(gitPath)
	if err != nil {
		t.Fatal(err)
	}
	main := plumbing.NewBranchReferenceName("main")
	current, err := repository.Reference(main, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = repository.Close(); err != nil {
		t.Fatal(err)
	}

	unpublished := mustObjectID(t, strings.Repeat("9", 64))
	update := &packp.UpdateRequests{Commands: []*packp.Command{{
		Name: main,
		Old:  current.Hash(),
		New:  unpublished,
	}}}
	update.Capabilities.Set(capability.ObjectFormat, gitSHA256ObjectFormat)
	var body bytes.Buffer
	if err = update.Encode(&body); err != nil {
		t.Fatal(err)
	}
	_, _ = body.WriteString("this is not a Git pack")

	response := httptest.NewRecorder()
	(Handler{Storage: store}).ServeHTTP(
		response,
		httptest.NewRequest(
			http.MethodPost,
			"/engineering/docs.git/git-receive-pack",
			&body,
		),
	)
	if response.Code != http.StatusConflict {
		t.Fatalf("malformed pack returned %d, want %d: %s", response.Code, http.StatusConflict, response.Body.String())
	}

	repository, err = gitformat.Open(gitPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repository.Close() }()
	ref, err := repository.Reference(main, false)
	if err != nil {
		t.Fatal(err)
	}
	if ref.Hash() != current.Hash() {
		t.Fatalf("main changed after malformed pack: %s -> %s", current.Hash(), ref.Hash())
	}
	if _, err = repository.Storer.EncodedObject(plumbing.AnyObject, unpublished); !errors.Is(err, plumbing.ErrObjectNotFound) {
		t.Fatalf("unpublished object entered live storage: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(gitPath))
	if err != nil {
		t.Fatal(err)
	}
	prefix := "." + filepath.Base(gitPath) + ".receive-"
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) {
			t.Fatalf("receive quarantine was not removed: %s", entry.Name())
		}
	}
}

type advertisementReferenceStorer struct {
	gitstorage.Storer

	references   []*plumbing.Reference
	referenceErr error
	iterErr      error
}

func (s *advertisementReferenceStorer) IterReferences() (storer.ReferenceIter, error) {
	if s.iterErr != nil {
		return nil, s.iterErr
	}
	return storer.NewReferenceSliceIter(s.references), nil
}

func (s *advertisementReferenceStorer) Reference(
	name plumbing.ReferenceName,
) (*plumbing.Reference, error) {
	if s.referenceErr != nil {
		return nil, s.referenceErr
	}
	return s.Storer.Reference(name)
}

type errorWriter struct {
	err error
}

func (w errorWriter) Write([]byte) (int, error) {
	return 0, w.err
}

var errTestWriteFailure = errors.New("test write failure")

type failAfterWriter struct {
	remaining int
}

func (w *failAfterWriter) Write(payload []byte) (int, error) {
	if w.remaining == 0 {
		return 0, errTestWriteFailure
	}
	w.remaining--
	return len(payload), nil
}
