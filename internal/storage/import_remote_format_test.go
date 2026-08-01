package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/define42/GitOne/internal/gitformat"
	"github.com/define42/GitOne/internal/repopath"
	"github.com/go-git/go-git/v6/plumbing"
	gitclient "github.com/go-git/go-git/v6/plumbing/client"
	formatcfg "github.com/go-git/go-git/v6/plumbing/format/config"
	"github.com/go-git/go-git/v6/plumbing/format/pktline"
	"github.com/go-git/go-git/v6/plumbing/protocol"
	"github.com/go-git/go-git/v6/plumbing/protocol/capability"
	"github.com/go-git/go-git/v6/plumbing/protocol/packp"
	gittransport "github.com/go-git/go-git/v6/plumbing/transport"
	githttp "github.com/go-git/go-git/v6/plumbing/transport/http"
)

func TestAdvertisedRemoteObjectFormat(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*capability.List)
		want    formatcfg.ObjectFormat
		wantErr bool
	}{
		{name: "missing means SHA-1", want: formatcfg.SHA1},
		{
			name: "explicit SHA-1",
			prepare: func(caps *capability.List) {
				caps.Add(capability.ObjectFormat, "sha1")
			},
			want: formatcfg.SHA1,
		},
		{
			name: "explicit SHA-256",
			prepare: func(caps *capability.List) {
				caps.Add(capability.ObjectFormat, "sha256")
			},
			want: formatcfg.SHA256,
		},
		{
			name: "bare capability",
			prepare: func(caps *capability.List) {
				caps.Add(capability.ObjectFormat)
			},
			wantErr: true,
		},
		{
			name: "unsupported format",
			prepare: func(caps *capability.List) {
				caps.Add(capability.ObjectFormat, "sha512")
			},
			wantErr: true,
		},
		{
			name: "multiple formats",
			prepare: func(caps *capability.List) {
				caps.Add(capability.ObjectFormat, "sha1", "sha256")
			},
			wantErr: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			caps := &capability.List{}
			if test.prepare != nil {
				test.prepare(caps)
			}
			got, err := advertisedRemoteObjectFormat(caps)
			if (err != nil) != test.wantErr {
				t.Fatalf("advertisedRemoteObjectFormat error = %v, wantErr %v", err, test.wantErr)
			}
			if !test.wantErr && got != test.want {
				t.Fatalf("advertisedRemoteObjectFormat = %s, want %s", got, test.want)
			}
		})
	}
}

func TestPreflightRemoteObjectFormatUsesProtocolV2ClientAndAuth(t *testing.T) {
	var requests atomic.Int32
	var mu sync.Mutex
	var protocolHeader, requestPath, requestService string
	var authenticated bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		username, password, ok := request.BasicAuth()
		mu.Lock()
		authenticated = ok && username == "importer" && password == "secret"
		protocolHeader = request.Header.Get("Git-Protocol")
		requestPath = request.URL.Path
		requestService = request.URL.Query().Get("service")
		mu.Unlock()
		writeRemoteFormatAdvertisement(t, writer, true, formatcfg.SHA256)
	}))
	defer server.Close()

	baseTransport := server.Client().Transport
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return baseTransport.RoundTrip(request)
	})}
	got, err := preflightRemoteObjectFormat(
		context.Background(),
		server.URL+"/repository.git",
		[]gitclient.Option{
			gitclient.WithHTTPClient(httpClient),
			gitclient.WithHTTPAuth(&githttp.BasicAuth{Username: "importer", Password: "secret"}),
		},
	)
	if err != nil {
		t.Fatalf("preflightRemoteObjectFormat: %v", err)
	}
	if got != formatcfg.SHA256 {
		t.Fatalf("object format = %s, want sha256", got)
	}
	if requests.Load() != 1 {
		t.Fatalf("HTTP requests = %d, want one discovery request", requests.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if !authenticated {
		t.Fatal("preflight did not apply the import Basic authentication option")
	}
	if protocolHeader != "version=2" {
		t.Fatalf("Git-Protocol = %q, want version=2", protocolHeader)
	}
	if requestPath != "/repository.git/info/refs" || requestService != gittransport.UploadPackService {
		t.Fatalf("discovery request = %s?service=%s", requestPath, requestService)
	}
}

func TestPreflightRemoteObjectFormatRejectsInvalidInput(t *testing.T) {
	t.Run("malformed URL", func(t *testing.T) {
		objectFormat, err := preflightRemoteObjectFormat(
			context.Background(),
			"http://[invalid-address",
			nil,
		)
		if err == nil {
			t.Fatal("preflight accepted a malformed remote URL")
		}
		if objectFormat != formatcfg.UnsetObjectFormat {
			t.Fatalf("failed preflight object format = %s, want unset", objectFormat)
		}
	})

	t.Run("unsupported advertisement", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(
			writer http.ResponseWriter,
			_ *http.Request,
		) {
			writeRemoteFormatAdvertisement(
				t,
				writer,
				true,
				formatcfg.ObjectFormat("sha512"),
			)
		}))
		defer server.Close()

		objectFormat, err := preflightRemoteObjectFormat(
			context.Background(),
			server.URL+"/repository.git",
			[]gitclient.Option{gitclient.WithHTTPClient(server.Client())},
		)
		if err == nil || !strings.Contains(err.Error(), "unsupported object-format") {
			t.Fatalf("unsupported-advertisement preflight error = %v", err)
		}
		if objectFormat != formatcfg.UnsetObjectFormat {
			t.Fatalf("failed preflight object format = %s, want unset", objectFormat)
		}
	})
}

func TestStrictFIPSRemotePreflightRejectsSHA1BeforeClone(t *testing.T) {
	if err := gitformat.RequireLegacySHA1(); err == nil {
		t.Skip("strict fips140=only mode is not active")
	}
	for _, explicit := range []bool{false, true} {
		name := "missing object-format"
		if explicit {
			name = "explicit SHA-1"
		}
		t.Run(name, func(t *testing.T) {
			var discoveries atomic.Int32
			var packRequests atomic.Int32
			var authenticated atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if username, password, ok := request.BasicAuth(); ok &&
					username == "importer" && password == "secret" {
					authenticated.Store(true)
				}
				if request.Method == http.MethodPost {
					packRequests.Add(1)
					http.Error(writer, "pack request must not occur", http.StatusInternalServerError)
					return
				}
				discoveries.Add(1)
				writeRemoteFormatAdvertisement(t, writer, explicit, formatcfg.SHA1)
			}))
			defer server.Close()

			policy, err := NewImportNetworkPolicy([]string{"127.0.0.1"})
			if err != nil {
				t.Fatalf("NewImportNetworkPolicy: %v", err)
			}
			ctx := WithImportNetworkPolicy(context.Background(), policy)
			store := Store{Root: t.TempDir()}
			_, err = store.stageRemoteRepository(ctx, ImportRepositoryOptions{
				URL:      server.URL + "/repository.git",
				Username: "importer",
				Password: "secret",
			})
			if !errors.Is(err, gitformat.ErrLegacySHA1Unavailable) {
				t.Fatalf("stageRemoteRepository error = %v, want ErrLegacySHA1Unavailable", err)
			}
			if discoveries.Load() != 1 || packRequests.Load() != 0 {
				t.Fatalf(
					"remote requests: discoveries=%d packs=%d, want 1 and 0",
					discoveries.Load(), packRequests.Load(),
				)
			}
			if !authenticated.Load() {
				t.Fatal("strict preflight did not send configured authentication")
			}
			entries, readErr := os.ReadDir(filepath.Join(store.Root, ".gitone", "imports"))
			if readErr != nil {
				t.Fatalf("read import staging: %v", readErr)
			}
			if len(entries) != 0 {
				t.Fatalf("failed preflight left %d staging entries", len(entries))
			}
		})
	}
}

func TestRemoteObjectFormatChangeRejectedBeforeFetch(t *testing.T) {
	for _, test := range []struct {
		name      string
		preflight formatcfg.ObjectFormat
		clone     formatcfg.ObjectFormat
	}{
		{name: "SHA-256 to SHA-1", preflight: formatcfg.SHA256, clone: formatcfg.SHA1},
		{name: "SHA-1 to SHA-256", preflight: formatcfg.SHA1, clone: formatcfg.SHA256},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.preflight == formatcfg.SHA1 {
				if err := gitformat.RequireLegacySHA1(); err != nil {
					t.Skip("strict FIPS-only mode rejects the initial SHA-1 advertisement")
				}
			}

			var discoveries atomic.Int32
			var lsRefsRequests atomic.Int32
			var fetchRequests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Method == http.MethodGet {
					objectFormat := test.preflight
					if discoveries.Add(1) > 1 {
						// Simulate a malicious or broken remote changing its object
						// format between preflight and go-git's own handshake.
						objectFormat = test.clone
					}
					writeRemoteFormatAdvertisement(t, writer, true, objectFormat)
					return
				}

				body, err := io.ReadAll(request.Body)
				if err != nil {
					t.Errorf("read upload-pack command: %v", err)
					http.Error(writer, "invalid request", http.StatusBadRequest)
					return
				}
				switch {
				case bytes.Contains(body, []byte("command=ls-refs\n")):
					lsRefsRequests.Add(1)
					writeRemoteRefs(t, writer, test.clone)
				case bytes.Contains(body, []byte("command=fetch\n")):
					fetchRequests.Add(1)
					http.Error(writer, "fetch must not occur", http.StatusInternalServerError)
				default:
					t.Errorf("unexpected upload-pack command %q", body)
					http.Error(writer, "unexpected command", http.StatusBadRequest)
				}
			}))
			defer server.Close()

			policy, err := NewImportNetworkPolicy([]string{"127.0.0.1"})
			if err != nil {
				t.Fatalf("NewImportNetworkPolicy: %v", err)
			}
			store := Store{Root: t.TempDir()}
			_, err = store.stageRemoteRepository(
				WithImportNetworkPolicy(context.Background(), policy),
				ImportRepositoryOptions{URL: server.URL + "/repository.git"},
			)
			if err == nil {
				t.Fatal("stageRemoteRepository succeeded after remote changed object format")
			}
			wantMismatch := "mismatched algorithms: client " + test.preflight.String() +
				"; server " + test.clone.String()
			var importErr *RemoteImportError
			if !errors.As(err, &importErr) || !strings.Contains(importErr.Err.Error(), wantMismatch) {
				t.Fatalf("stageRemoteRepository error = %v, want %q", err, wantMismatch)
			}
			if discoveries.Load() != 2 {
				t.Fatalf("discovery requests = %d, want preflight and clone handshakes", discoveries.Load())
			}
			if lsRefsRequests.Load() != 1 {
				t.Fatalf("ls-refs requests = %d, want one", lsRefsRequests.Load())
			}
			if fetchRequests.Load() != 0 {
				t.Fatalf("fetch requests = %d, want none", fetchRequests.Load())
			}
			entries, readErr := os.ReadDir(filepath.Join(store.Root, ".gitone", "imports"))
			if readErr != nil {
				t.Fatalf("read import staging: %v", readErr)
			}
			if len(entries) != 0 {
				t.Fatalf("failed clone left %d staging entries", len(entries))
			}
		})
	}
}

func TestStageRemoteSHA256RepositoryPinsAndValidatesFormat(t *testing.T) {
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
	defer func() { _ = source.Close() }()
	sourceHead, err := source.Head()
	if err != nil {
		t.Fatal(err)
	}

	var discoveries atomic.Int32
	var lsRefsRequests atomic.Int32
	var fetchRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-cache")
		if request.Method == http.MethodGet {
			discoveries.Add(1)
			writer.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
			if serveErr := gittransport.UploadPack(
				request.Context(),
				source.Storer,
				nil,
				testResponseWriteCloser{Writer: writer},
				&gittransport.UploadPackRequest{
					GitProtocol:   request.Header.Get("Git-Protocol"),
					AdvertiseRefs: true,
					StatelessRPC:  true,
				},
			); serveErr != nil {
				http.Error(writer, serveErr.Error(), http.StatusInternalServerError)
			}
			return
		}

		body, readErr := io.ReadAll(request.Body)
		if readErr != nil {
			http.Error(writer, readErr.Error(), http.StatusBadRequest)
			return
		}
		switch {
		case bytes.Contains(body, []byte("command=ls-refs\n")):
			lsRefsRequests.Add(1)
		case bytes.Contains(body, []byte("command=fetch\n")):
			fetchRequests.Add(1)
		}
		writer.Header().Set("Content-Type", "application/x-git-upload-pack-result")
		if serveErr := gittransport.UploadPack(
			request.Context(),
			source.Storer,
			io.NopCloser(bytes.NewReader(body)),
			testResponseWriteCloser{Writer: writer},
			&gittransport.UploadPackRequest{
				GitProtocol:  request.Header.Get("Git-Protocol"),
				StatelessRPC: true,
			},
		); serveErr != nil {
			http.Error(writer, serveErr.Error(), http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	policy, err := NewImportNetworkPolicy([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	targetStore := Store{Root: t.TempDir()}
	staged, err := targetStore.stageRemoteRepository(
		WithImportNetworkPolicy(context.Background(), policy),
		ImportRepositoryOptions{URL: server.URL + "/repository.git"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(staged.root) }()

	if discoveries.Load() != 2 {
		t.Fatalf("discovery requests = %d, want preflight and clone handshakes", discoveries.Load())
	}
	if lsRefsRequests.Load() != 1 || fetchRequests.Load() == 0 {
		t.Fatalf(
			"upload-pack requests: ls-refs=%d fetch=%d, want one and at least one",
			lsRefsRequests.Load(),
			fetchRequests.Load(),
		)
	}
	stagedRepository, err := gitformat.Open(staged.gitPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stagedRepository.Close() }()
	if err = gitformat.ValidateReachable(stagedRepository); err != nil {
		t.Fatalf("validate staged SHA-256 repository: %v", err)
	}
	stagedHead, err := stagedRepository.Head()
	if err != nil {
		t.Fatal(err)
	}
	if stagedHead.Hash() != sourceHead.Hash() || !gitformat.IsSHA256OID(stagedHead.Hash().String()) {
		t.Fatalf("staged HEAD = %s, want SHA-256 %s", stagedHead.Hash(), sourceHead.Hash())
	}
	if _, err = os.Stat(filepath.Join(staged.lfsPath, "objects")); err != nil {
		t.Fatalf("staged LFS object directory: %v", err)
	}
}

type testResponseWriteCloser struct {
	io.Writer
}

func (testResponseWriteCloser) Close() error { return nil }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func writeRemoteFormatAdvertisement(
	t *testing.T,
	writer http.ResponseWriter,
	explicit bool,
	objectFormat formatcfg.ObjectFormat,
) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
	if err := (&packp.SmartReply{Service: gittransport.UploadPackService}).Encode(writer); err != nil {
		t.Errorf("encode smart reply: %v", err)
		return
	}
	capabilities := capability.List{}
	capabilities.Add(capability.LsRefs)
	capabilities.Add(capability.FetchCmd)
	if explicit {
		capabilities.Add(capability.ObjectFormat, objectFormat.String())
	}
	if err := (&packp.CapabilityAdv{
		Version:      protocol.V2,
		Capabilities: capabilities,
	}).Encode(writer); err != nil {
		t.Errorf("encode capability advertisement: %v", err)
	}
}

func writeRemoteRefs(
	t *testing.T,
	writer http.ResponseWriter,
	objectFormat formatcfg.ObjectFormat,
) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/x-git-upload-pack-result")
	hashSize := formatcfg.SHA1HexSize
	if objectFormat == formatcfg.SHA256 {
		hashSize = formatcfg.SHA256HexSize
	}
	response := &packp.LsRefsOutput{References: []*plumbing.Reference{
		plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName("main")),
		plumbing.NewHashReference(
			plumbing.NewBranchReferenceName("main"),
			plumbing.NewHash(strings.Repeat("1", hashSize)),
		),
	}}
	if err := response.Encode(writer); err != nil {
		t.Errorf("encode ls-refs response: %v", err)
		return
	}
	if err := pktline.WriteFlush(writer); err != nil {
		t.Errorf("encode ls-refs flush: %v", err)
		return
	}
	if err := pktline.WriteResponseEnd(writer); err != nil {
		t.Errorf("encode ls-refs response-end: %v", err)
	}
}
