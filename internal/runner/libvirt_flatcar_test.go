package runner

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
	"time"

	libvirt "github.com/digitalocean/go-libvirt"
	"golang.org/x/sys/unix"
)

type flatcarRefreshRunner struct {
	fakeLibvirtRPCClient
	mu        sync.Mutex
	refreshes int
}

type flatcarRoundTripFunc func(*http.Request) (*http.Response, error)

func (f flatcarRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type flatcarReaderFunc func([]byte) (int, error)

func (f flatcarReaderFunc) Read(buffer []byte) (int, error) {
	return f(buffer)
}

func (r *flatcarRefreshRunner) StoragePoolRefresh(libvirt.StoragePool, uint32) error {
	r.mu.Lock()
	r.refreshes++
	r.mu.Unlock()
	return nil
}

func (r *flatcarRefreshRunner) refreshCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.refreshes
}

func newFlatcarTestProvider(
	poolPath string,
	imageURL string,
	digest string,
	client *http.Client,
	rpcClient libvirtRPCClient,
) *libvirtRPCProvider {
	return &libvirtRPCProvider{
		config: LibvirtConfig{
			URI:             "test:///system",
			PoolName:        "test-pool",
			PoolPath:        poolPath,
			BaseVolumeName:  "flatcar-base.qcow2",
			BaseImageURL:    imageURL,
			BaseImageSHA512: digest,
		},
		httpClient: client,
		client:     rpcClient,
		pool:       libvirt.StoragePool{Name: "test-pool"},
	}
}

func TestEnsureFlatcarBaseImageDownloadsVerifiesAndReusesImage(t *testing.T) {
	contents := []byte("test qcow2 image contents")
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		requests.Add(1)
		if request.Header.Get("Accept-Encoding") != "identity" {
			t.Errorf("Accept-Encoding = %q, want identity", request.Header.Get("Accept-Encoding"))
		}
		_, _ = response.Write(contents)
	}))
	defer server.Close()

	poolPath := t.TempDir()
	runner := &flatcarRefreshRunner{}
	provider := newFlatcarTestProvider(
		poolPath,
		server.URL+"/flatcar.img",
		flatcarTestSHA512(contents),
		server.Client(),
		runner,
	)
	stagingPath := filepath.Join(poolPath, flatcarStagingDirectoryName)
	if err := os.Mkdir(stagingPath, 0o700); err != nil {
		t.Fatal(err)
	}
	stalePath := filepath.Join(stagingPath, provider.flatcarStagingFilePrefix()+"crashed.download")
	if err := os.WriteFile(stalePath, []byte("partial old download"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := provider.ensureFlatcarBaseImage(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := provider.ensureFlatcarBaseImage(context.Background()); err != nil {
		t.Fatalf("reuse verified Flatcar image: %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("download requests = %d, want 1", requests.Load())
	}
	if runner.refreshCount() != 2 {
		t.Fatalf("storage-pool refreshes = %d, want 2", runner.refreshCount())
	}

	path := filepath.Join(poolPath, provider.config.BaseVolumeName)
	downloaded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(downloaded, contents) {
		t.Fatalf("downloaded Flatcar image = %q, want %q", downloaded, contents)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("downloaded Flatcar image mode = %#o, want 0644", info.Mode().Perm())
	}
	temporary, err := filepath.Glob(filepath.Join(
		poolPath,
		flatcarStagingDirectoryName,
		"gitone-*.download",
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(temporary) != 0 {
		t.Fatalf("temporary downloads were not removed: %#v", temporary)
	}
	stagingInfo, err := os.Stat(stagingPath)
	if err != nil {
		t.Fatal(err)
	}
	if !stagingInfo.IsDir() || stagingInfo.Mode().Perm() != 0o700 {
		t.Fatalf("Flatcar staging mode = %v, want directory 0700", stagingInfo.Mode())
	}
}

func TestEnsureFlatcarBaseImageRejectsBadDownloadWithoutPublishing(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		_ *http.Request,
	) {
		_, _ = io.WriteString(response, "tampered image")
	}))
	defer server.Close()

	poolPath := t.TempDir()
	provider := newFlatcarTestProvider(
		poolPath,
		server.URL+"/flatcar.img",
		flatcarTestSHA512([]byte("expected image")),
		server.Client(),
		&flatcarRefreshRunner{},
	)
	err := provider.ensureFlatcarBaseImage(context.Background())
	if err == nil || !strings.Contains(err.Error(), "SHA-512 mismatch") {
		t.Fatalf("bad image error = %v, want SHA-512 mismatch", err)
	}
	if _, err = os.Lstat(filepath.Join(poolPath, provider.config.BaseVolumeName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bad image was published or inspect failed: %v", err)
	}
	temporary, globErr := filepath.Glob(filepath.Join(
		poolPath,
		flatcarStagingDirectoryName,
		"gitone-*.download",
	))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(temporary) != 0 {
		t.Fatalf("failed download left temporary files: %#v", temporary)
	}
}

func TestEnsureFlatcarBaseImageReportsHTTPFailure(t *testing.T) {
	server := httptest.NewTLSServer(http.NotFoundHandler())
	defer server.Close()

	poolPath := t.TempDir()
	provider := newFlatcarTestProvider(
		poolPath,
		server.URL+"/missing.img",
		flatcarTestSHA512([]byte("unused")),
		server.Client(),
		&flatcarRefreshRunner{},
	)
	err := provider.ensureFlatcarBaseImage(context.Background())
	if err == nil || !strings.Contains(err.Error(), "404 Not Found") {
		t.Fatalf("HTTP failure = %v, want 404 status", err)
	}
	if _, err = os.Lstat(filepath.Join(poolPath, provider.config.BaseVolumeName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("HTTP failure published an image or inspect failed: %v", err)
	}
}

func TestFlatcarHTTPClientAllowsHTTPSRedirect(t *testing.T) {
	contents := []byte("redirected Flatcar image")
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		requests.Add(1)
		switch request.URL.Path {
		case "/redirect":
			http.Redirect(response, request, "/image", http.StatusFound)
		case "/image":
			if referer := request.Header.Get("Referer"); referer != "" {
				t.Errorf("redirected Flatcar request Referer = %q, want empty", referer)
			}
			_, _ = response.Write(contents)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	client := newFlatcarHTTPClient()
	client.Transport = server.Client().Transport
	poolPath := t.TempDir()
	provider := newFlatcarTestProvider(
		poolPath,
		server.URL+"/redirect?token=must-not-leak",
		flatcarTestSHA512(contents),
		client,
		&flatcarRefreshRunner{},
	)
	path := filepath.Join(poolPath, provider.config.BaseVolumeName)
	if err := provider.ensureFlatcarBaseImageFile(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 {
		t.Fatalf("HTTPS redirect requests = %d, want 2", requests.Load())
	}
}

func TestFlatcarHTTPClientRejectsHTTPSDowngrade(t *testing.T) {
	var insecureRequests atomic.Int32
	insecureServer := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		_ *http.Request,
	) {
		insecureRequests.Add(1)
		_, _ = io.WriteString(response, "insecure image")
	}))
	defer insecureServer.Close()
	secureServer := httptest.NewTLSServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		http.Redirect(response, request, insecureServer.URL+"/image", http.StatusFound)
	}))
	defer secureServer.Close()

	client := newFlatcarHTTPClient()
	client.Transport = secureServer.Client().Transport
	poolPath := t.TempDir()
	provider := newFlatcarTestProvider(
		poolPath,
		secureServer.URL+"/redirect",
		flatcarTestSHA512([]byte("insecure image")),
		client,
		&flatcarRefreshRunner{},
	)
	path := filepath.Join(poolPath, provider.config.BaseVolumeName)
	err := provider.ensureFlatcarBaseImageFile(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), "absolute HTTPS URL") {
		t.Fatalf("HTTPS downgrade error = %v, want HTTPS policy failure", err)
	}
	if insecureRequests.Load() != 0 {
		t.Fatalf("HTTPS downgrade reached HTTP server %d times", insecureRequests.Load())
	}
	if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("HTTPS downgrade published an image or inspect failed: %v", statErr)
	}
}

func TestFlatcarHTTPClientBoundsRedirects(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		requests.Add(1)
		http.Redirect(response, request, "/loop", http.StatusFound)
	}))
	defer server.Close()

	client := newFlatcarHTTPClient()
	client.Transport = server.Client().Transport
	response, err := client.Get(server.URL + "/loop")
	if response != nil {
		_ = response.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "too many Flatcar base image redirects") {
		t.Fatalf("redirect-loop error = %v, want redirect limit", err)
	}
	if requests.Load() != 10 {
		t.Fatalf("redirect-loop requests = %d, want 10", requests.Load())
	}
}

func TestExistingFlatcarBaseImageMismatchIsNeverDownloadedOrOverwritten(t *testing.T) {
	original := []byte("existing corrupt image")
	expected := []byte("expected image")
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		_ *http.Request,
	) {
		requests.Add(1)
		_, _ = response.Write(expected)
	}))
	defer server.Close()

	poolPath := t.TempDir()
	path := filepath.Join(poolPath, "flatcar-base.qcow2")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &flatcarRefreshRunner{}
	provider := newFlatcarTestProvider(
		poolPath,
		server.URL+"/flatcar.img",
		flatcarTestSHA512(expected),
		server.Client(),
		runner,
	)
	err := provider.ensureFlatcarBaseImage(context.Background())
	if err == nil || !strings.Contains(err.Error(), "SHA-512 mismatch") {
		t.Fatalf("existing corrupt image error = %v, want SHA-512 mismatch", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("existing corrupt image caused %d download requests", requests.Load())
	}
	if runner.refreshCount() != 0 {
		t.Fatalf("existing corrupt image caused %d pool refreshes", runner.refreshCount())
	}
	remaining, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(remaining, original) {
		t.Fatalf("existing corrupt image was overwritten: got %q", remaining)
	}
}

func TestCanceledFlatcarDownloadCleansTemporaryFileAndReleasesLock(t *testing.T) {
	requestStarted := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		response.Header().Set("Content-Type", "application/octet-stream")
		_, _ = response.Write([]byte("partial image"))
		if flusher, ok := response.(http.Flusher); ok {
			flusher.Flush()
		}
		close(requestStarted)
		<-request.Context().Done()
	}))
	defer server.Close()

	poolPath := t.TempDir()
	provider := newFlatcarTestProvider(
		poolPath,
		server.URL+"/flatcar.img",
		flatcarTestSHA512([]byte("complete image")),
		server.Client(),
		&flatcarRefreshRunner{},
	)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- provider.ensureFlatcarBaseImage(ctx)
	}()
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("partial Flatcar download did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled Flatcar download returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled Flatcar download did not stop")
	}
	path := filepath.Join(poolPath, provider.config.BaseVolumeName)
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled download published an image or inspect failed: %v", err)
	}
	temporary, err := filepath.Glob(filepath.Join(
		poolPath,
		flatcarStagingDirectoryName,
		"gitone-*.download",
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(temporary) != 0 {
		t.Fatalf("cancelled download left temporary files: %#v", temporary)
	}
	lock, err := provider.acquireFlatcarBaseImageLock(context.Background())
	if err != nil {
		t.Fatalf("reacquire Flatcar image lock after cancellation: %v", err)
	}
	if err = releaseLibvirtFileLock(lock); err != nil {
		t.Fatalf("release reacquired Flatcar image lock: %v", err)
	}
}

func TestFlatcarDownloadCancellationWinsOverCleanEOF(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	partialWritten := false
	client := &http.Client{Transport: flatcarRoundTripFunc(func(
		request *http.Request,
	) (*http.Response, error) {
		body := flatcarReaderFunc(func(buffer []byte) (int, error) {
			if !partialWritten {
				partialWritten = true
				return copy(buffer, "partial image"), nil
			}
			cancel()
			return 0, io.EOF
		})
		return &http.Response{
			Status:        "200 OK",
			StatusCode:    http.StatusOK,
			Header:        make(http.Header),
			Body:          io.NopCloser(body),
			ContentLength: -1,
			Request:       request,
		}, nil
	})}
	poolPath := t.TempDir()
	provider := newFlatcarTestProvider(
		poolPath,
		"https://example.test/flatcar.img",
		flatcarTestSHA512([]byte("complete image")),
		client,
		&flatcarRefreshRunner{},
	)

	err := provider.ensureFlatcarBaseImage(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Flatcar download returned %v", err)
	}
	path := filepath.Join(poolPath, provider.config.BaseVolumeName)
	if _, err = os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled download published an image or inspect failed: %v", err)
	}
}

func TestEnsureFlatcarBaseImageConcurrentCallersDownloadOnce(t *testing.T) {
	contents := bytes.Repeat([]byte("qcow2"), 4096)
	var requests atomic.Int32
	requestStarted := make(chan struct{})
	releaseResponse := make(chan struct{})
	var signalRequest sync.Once
	server := httptest.NewTLSServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		_ *http.Request,
	) {
		requests.Add(1)
		signalRequest.Do(func() { close(requestStarted) })
		<-releaseResponse
		_, _ = response.Write(contents)
	}))
	defer server.Close()

	const callers = 6
	poolPath := t.TempDir()
	runner := &flatcarRefreshRunner{}
	provider := newFlatcarTestProvider(
		poolPath,
		server.URL+"/flatcar.img",
		flatcarTestSHA512(contents),
		server.Client(),
		runner,
	)
	start := make(chan struct{})
	results := make(chan error, callers)
	for range callers {
		go func() {
			<-start
			results <- provider.ensureFlatcarBaseImage(context.Background())
		}()
	}
	close(start)
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("Flatcar download did not start")
	}
	close(releaseResponse)
	var firstError error
	for range callers {
		if err := <-results; err != nil && firstError == nil {
			firstError = err
		}
	}
	if firstError != nil {
		t.Fatalf("concurrent Flatcar image check: %v", firstError)
	}
	if requests.Load() != 1 {
		t.Fatalf("concurrent download requests = %d, want 1", requests.Load())
	}
	if runner.refreshCount() != callers {
		t.Fatalf("concurrent pool refreshes = %d, want %d", runner.refreshCount(), callers)
	}
}

func TestFlatcarBaseImageLockUsesDirectoryAndVolumeName(t *testing.T) {
	poolPath := t.TempDir()
	first := newFlatcarTestProvider(
		poolPath,
		"https://one.example/flatcar.img",
		DefaultFlatcarBaseImageSHA512,
		nil,
		&flatcarRefreshRunner{},
	)
	second := newFlatcarTestProvider(
		poolPath,
		"https://two.example/flatcar.img",
		DefaultFlatcarBaseImageSHA512,
		nil,
		&flatcarRefreshRunner{},
	)
	second.config.URI = "qemu+ssh://alias/system"
	second.config.PoolName = "same-directory-alias"

	lock, err := first.acquireFlatcarBaseImageLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if releaseErr := releaseLibvirtFileLock(lock); releaseErr != nil {
			t.Errorf("release Flatcar test lock: %v", releaseErr)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	otherLock, err := second.acquireFlatcarBaseImageLock(ctx)
	if otherLock != nil {
		_ = releaseLibvirtFileLock(otherLock)
		t.Fatal("libvirt aliases acquired separate locks for the same base file")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second lock error = %v, want context deadline", err)
	}
}

func TestValidateFlatcarPoolDirectoryRejectsUnsafeNamespace(t *testing.T) {
	t.Run("group writable", func(t *testing.T) {
		path := t.TempDir()
		if err := os.Chmod(path, 0o770); err != nil {
			t.Fatal(err)
		}
		err := validateFlatcarPoolDirectory(path)
		if err == nil || !strings.Contains(err.Error(), "writable by group or others") {
			t.Fatalf("group-writable pool error = %v", err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		parent := t.TempDir()
		if err := os.Chmod(parent, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(parent, "target")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(parent, "link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if err := validateFlatcarPoolDirectory(link); err == nil ||
			!strings.Contains(err.Error(), "without symlinks") {
			t.Fatalf("symlinked pool error = %v", err)
		}
	})
}

func TestValidateFlatcarBaseImageRejectsUntrustedOwner(t *testing.T) {
	untrustedUID := uint32(os.Geteuid()) + 1
	if untrustedUID == 0 {
		untrustedUID = 1
	}
	err := validateFlatcarBaseImageStat(unix.Stat_t{
		Mode: unix.S_IFREG | 0o444,
		Uid:  untrustedUID,
		Size: 1,
	})
	if err == nil || !strings.Contains(err.Error(), "owned by root or the runner user") {
		t.Fatalf("third-party-owned base image error = %v", err)
	}
}

func TestExistingFlatcarBaseImageHashHonorsCancellation(t *testing.T) {
	contents := bytes.Repeat([]byte("image"), 1024)
	poolPath := t.TempDir()
	path := filepath.Join(poolPath, "flatcar-base.qcow2")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	provider := newFlatcarTestProvider(
		poolPath,
		"https://example.test/flatcar.img",
		flatcarTestSHA512(contents),
		nil,
		&flatcarRefreshRunner{},
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := provider.ensureFlatcarBaseImageFile(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled existing-image hash returned %v", err)
	}
}
