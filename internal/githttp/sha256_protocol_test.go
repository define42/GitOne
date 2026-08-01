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
	"github.com/go-git/go-git/v6/plumbing/protocol/capability"
	"github.com/go-git/go-git/v6/plumbing/protocol/packp"
)

func TestSmartHTTPAdvertisesSHA256ObjectFormat(t *testing.T) {
	handler := newSHA256SmartHTTPHandler(t, true)
	for _, service := range []string{"git-upload-pack", "git-receive-pack"} {
		t.Run(service, func(t *testing.T) {
			request := httptest.NewRequest(
				http.MethodGet,
				"/engineering/docs.git/info/refs?service="+service,
				nil,
			)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("advertisement returned %d: %s", response.Code, response.Body.String())
			}
			reader := bytes.NewReader(response.Body.Bytes())
			var smartReply packp.SmartReply
			if err := smartReply.Decode(reader); err != nil {
				t.Fatalf("decode smart reply: %v", err)
			}
			if smartReply.Service != service {
				t.Fatalf("service = %q, want %q", smartReply.Service, service)
			}
			var advertisement packp.AdvRefs
			if err := advertisement.Decode(reader); err != nil {
				t.Fatalf("decode advertisement: %v", err)
			}
			formats := advertisement.Capabilities.Get(capability.ObjectFormat)
			if len(formats) != 1 || formats[0] != gitSHA256ObjectFormat {
				t.Fatalf("object-format = %v, want [sha256]", formats)
			}
			if service == "git-receive-pack" {
				for _, expected := range []capability.Capability{
					capability.Agent,
					capability.OFSDelta,
					capability.Sideband64k,
					capability.NoThin,
					capability.DeleteRefs,
					capability.ReportStatus,
					capability.PushOptions,
					capability.Quiet,
				} {
					if !advertisement.Capabilities.Supports(expected) {
						t.Errorf("receive advertisement omitted %q", expected)
					}
				}
			}
			for _, reference := range advertisement.References {
				if reference.Hash().Size() != gitSHA256ObjectIDSize {
					t.Fatalf("%s advertised non-SHA-256 object ID %s", reference.Name(), reference.Hash())
				}
			}
		})
	}
}

func TestEmptyReceiveAdvertisementUsesSHA256ZeroObjectID(t *testing.T) {
	handler := newSHA256SmartHTTPHandler(t, false)
	request := httptest.NewRequest(
		http.MethodGet,
		"/engineering/docs.git/info/refs?service=git-receive-pack",
		nil,
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("advertisement returned %d: %s", response.Code, response.Body.String())
	}
	if !strings.Contains(
		response.Body.String(),
		gitSHA256ZeroObjectID+" capabilities^{}\x00",
	) {
		t.Fatalf("empty advertisement did not contain a SHA-256 zero object ID: %q", response.Body.String())
	}
	reader := bytes.NewReader(response.Body.Bytes())
	var smartReply packp.SmartReply
	if err := smartReply.Decode(reader); err != nil {
		t.Fatalf("decode smart reply: %v", err)
	}
	var advertisement packp.AdvRefs
	if err := advertisement.Decode(reader); err != nil {
		t.Fatalf("decode advertisement: %v", err)
	}
	if !advertisement.IsEmpty() {
		t.Fatalf("empty repository advertised references: %#v", advertisement.References)
	}
	formats := advertisement.Capabilities.Get(capability.ObjectFormat)
	if len(formats) != 1 || formats[0] != gitSHA256ObjectFormat {
		t.Fatalf("object-format = %v, want [sha256]", formats)
	}
}

func TestReceivePackRejectsWrongObjectIDsAndShallowPushesBeforeRepositoryAccess(t *testing.T) {
	sha1Zero := mustObjectID(t, strings.Repeat("0", 40))
	sha256Old := mustObjectID(t, strings.Repeat("3", 64))
	sha256New := mustObjectID(t, strings.Repeat("4", 64))

	for _, test := range []struct {
		name         string
		old          plumbing.Hash
		new          plumbing.Hash
		shallows     []plumbing.Hash
		objectFormat string
		want         string
	}{
		{
			name:         "SHA-1 old zero object ID",
			old:          sha1Zero,
			new:          sha256New,
			objectFormat: gitSHA256ObjectFormat,
			want:         "old object ID must use SHA-256",
		},
		{
			name:         "SHA-1 new zero object ID",
			old:          sha256Old,
			new:          sha1Zero,
			objectFormat: gitSHA256ObjectFormat,
			want:         "new object ID must use SHA-256",
		},
		{
			name:         "SHA-1 object-format capability",
			old:          sha256Old,
			new:          sha256New,
			objectFormat: "sha1",
			want:         "object-format must be sha256",
		},
		{
			name:         "SHA-1 shallow object ID",
			old:          sha256Old,
			new:          sha256New,
			shallows:     []plumbing.Hash{sha1Zero},
			objectFormat: gitSHA256ObjectFormat,
			want:         "shallow pushes are not supported",
		},
		{
			name:         "SHA-256 shallow push",
			old:          sha256Old,
			new:          sha256New,
			shallows:     []plumbing.Hash{sha256Old},
			objectFormat: gitSHA256ObjectFormat,
			want:         "shallow pushes are not supported",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			update := &packp.UpdateRequests{
				Shallows: test.shallows,
				Commands: []*packp.Command{{
					Name: plumbing.NewBranchReferenceName("main"),
					Old:  test.old,
					New:  test.new,
				}},
			}
			if test.objectFormat != "" {
				update.Capabilities.Set(capability.ObjectFormat, test.objectFormat)
			}
			var body bytes.Buffer
			if err := update.Encode(&body); err != nil {
				t.Fatal(err)
			}

			handler := Handler{Storage: storage.Store{Root: t.TempDir()}}
			request := httptest.NewRequest(
				http.MethodPost,
				"/engineering/docs.git/git-receive-pack",
				&body,
			)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != http.StatusConflict {
				t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusConflict, response.Body.String())
			}
			if !strings.Contains(response.Body.String(), test.want) {
				t.Fatalf("response = %q, want containing %q", response.Body.String(), test.want)
			}
		})
	}
}

func TestReceivePackAllowsOmittedObjectFormatOnlyWithSHA256ObjectIDs(t *testing.T) {
	request := &packp.UpdateRequests{Commands: []*packp.Command{{
		Name: plumbing.NewBranchReferenceName("main"),
		Old:  mustObjectID(t, strings.Repeat("3", 64)),
		New:  mustObjectID(t, strings.Repeat("4", 64)),
	}}}
	if err := validateReceiveCapabilities(&request.Capabilities); err != nil {
		t.Fatalf("omitted object-format capability: %v", err)
	}
	if err := validateReceiveObjectIDs(request); err != nil {
		t.Fatalf("SHA-256 object IDs: %v", err)
	}
}

func TestReceivePackDeletesReferenceWithSHA256ZeroObjectID(t *testing.T) {
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

	update := &packp.UpdateRequests{Commands: []*packp.Command{{
		Name: main,
		Old:  current.Hash(),
		New:  mustObjectID(t, gitSHA256ZeroObjectID),
	}}}
	update.Capabilities.Set(capability.ObjectFormat, gitSHA256ObjectFormat)
	update.Capabilities.Set(capability.ReportStatus)
	var body bytes.Buffer
	if err = update.Encode(&body); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	(Handler{Storage: store}).ServeHTTP(
		response,
		httptest.NewRequest(
			http.MethodPost,
			"/engineering/docs.git/git-receive-pack",
			&body,
		),
	)
	if response.Code != http.StatusOK {
		t.Fatalf("delete returned %d: %s", response.Code, response.Body.String())
	}

	repository, err = gitformat.Open(gitPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repository.Close() }()
	if _, err = repository.Reference(main, false); !errors.Is(err, plumbing.ErrReferenceNotFound) {
		t.Fatalf("deleted reference remains: %v", err)
	}
}

func newSHA256SmartHTTPHandler(t *testing.T, withReference bool) Handler {
	t.Helper()
	root := t.TempDir()
	repositoryPath := filepath.Join(root, "engineering", "docs.git")
	if err := os.MkdirAll(filepath.Dir(repositoryPath), 0o750); err != nil {
		t.Fatal(err)
	}
	main := plumbing.NewBranchReferenceName("main")
	repository, err := gitformat.Init(repositoryPath, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = repository.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, main)); err != nil {
		t.Fatal(err)
	}
	if withReference {
		hash := mustObjectID(t, strings.Repeat("a", 64))
		if err = repository.Storer.SetReference(plumbing.NewHashReference(main, hash)); err != nil {
			t.Fatal(err)
		}
	}
	return Handler{Storage: storage.Store{Root: root}}
}

func mustObjectID(t *testing.T, hex string) plumbing.Hash {
	t.Helper()
	objectID, ok := plumbing.FromHex(hex)
	if !ok {
		t.Fatalf("invalid object ID %q", hex)
	}
	return objectID
}
