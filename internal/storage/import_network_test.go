package storage

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type importResolverFunc func(
	context.Context,
	string,
	string,
) ([]netip.Addr, error)

func (f importResolverFunc) LookupNetIP(
	ctx context.Context,
	network string,
	host string,
) ([]netip.Addr, error) {
	return f(ctx, network, host)
}

type importDialerFunc func(context.Context, string, string) (net.Conn, error)

func (f importDialerFunc) DialContext(
	ctx context.Context,
	network string,
	address string,
) (net.Conn, error) {
	return f(ctx, network, address)
}

func requireBlockedImportAddress(t *testing.T, err error) {
	t.Helper()
	var blocked *ImportAddressError
	if !errors.As(err, &blocked) {
		t.Fatalf("error = %v, want ImportAddressError", err)
	}
}

func TestImportNetworkPolicyBlocksNonPublicAddresses(t *testing.T) {
	policy, err := NewImportNetworkPolicy(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{
		"0.0.0.0",
		"10.0.0.1",
		"100.100.100.200",
		"127.0.0.1",
		"169.254.169.254",
		"172.16.0.1",
		"192.168.0.1",
		"198.18.0.1",
		"224.0.0.1",
		"240.0.0.1",
		"[::1]",
		"[::ffff:127.0.0.1]",
		"[64:ff9b::7f00:1]",
		"[fc00::1]",
		"[fe80::1]",
	} {
		t.Run(address, func(t *testing.T) {
			err := policy.ValidateURL(
				context.Background(),
				"http://"+address+"/repository.git",
			)
			requireBlockedImportAddress(t, err)
		})
	}
}

func TestImportNetworkPolicyValidatesEveryDNSAddress(t *testing.T) {
	policy, err := NewImportNetworkPolicy(nil)
	if err != nil {
		t.Fatal(err)
	}
	policy.resolver = importResolverFunc(func(
		_ context.Context,
		_ string,
		host string,
	) ([]netip.Addr, error) {
		switch host {
		case "public.example":
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		case "mixed.example":
			return []netip.Addr{
				netip.MustParseAddr("8.8.8.8"),
				netip.MustParseAddr("127.0.0.1"),
			}, nil
		default:
			return nil, errors.New("unexpected host")
		}
	})
	if err = policy.ValidateURL(
		context.Background(),
		"https://public.example/repository.git",
	); err != nil {
		t.Fatalf("public host rejected: %v", err)
	}
	err = policy.ValidateURL(
		context.Background(),
		"https://mixed.example/repository.git",
	)
	requireBlockedImportAddress(t, err)
}

func TestImportNetworkPolicyAllowlistOverridesDefaultBlocks(t *testing.T) {
	policy, err := NewImportNetworkPolicy([]string{
		"internal.example",
		"10.20.0.0/16",
		"127.0.0.1",
	})
	if err != nil {
		t.Fatal(err)
	}
	policy.resolver = importResolverFunc(func(
		_ context.Context,
		_ string,
		host string,
	) ([]netip.Addr, error) {
		switch host {
		case "internal.example":
			return []netip.Addr{netip.MustParseAddr("192.168.10.4")}, nil
		case "service.example":
			return []netip.Addr{netip.MustParseAddr("10.20.30.40")}, nil
		default:
			return nil, errors.New("unexpected host")
		}
	})
	for _, rawURL := range []string{
		"http://internal.example/repository.git",
		"http://service.example/repository.git",
		"http://127.0.0.1/repository.git",
	} {
		if err = policy.ValidateURL(context.Background(), rawURL); err != nil {
			t.Fatalf("allowlisted URL %q rejected: %v", rawURL, err)
		}
	}
}

func TestImportNetworkPolicyRejectsInvalidAllowlistEntries(t *testing.T) {
	for _, entry := range []string{
		"https://example.com",
		"example.com:443",
		"*.example.com",
		"bad host",
		"/internal",
	} {
		t.Run(entry, func(t *testing.T) {
			if _, err := NewImportNetworkPolicy([]string{entry}); err == nil {
				t.Fatalf("invalid allowlist entry %q was accepted", entry)
			}
		})
	}
}

func TestImportDialDefendsAgainstDNSRebinding(t *testing.T) {
	policy, err := NewImportNetworkPolicy(nil)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	lookups := 0
	policy.resolver = importResolverFunc(func(
		_ context.Context,
		_ string,
		_ string,
	) ([]netip.Addr, error) {
		mu.Lock()
		defer mu.Unlock()
		lookups++
		if lookups == 1 {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	})
	dialed := false
	policy.dialer = importDialerFunc(func(
		context.Context,
		string,
		string,
	) (net.Conn, error) {
		dialed = true
		return nil, errors.New("must not dial")
	})
	if err = policy.ValidateURL(
		context.Background(),
		"https://rebind.example/repository.git",
	); err != nil {
		t.Fatalf("initial public resolution rejected: %v", err)
	}
	ctx := WithImportNetworkPolicy(context.Background(), policy)
	_, err = importDialContext(ctx, "tcp", "rebind.example:443")
	requireBlockedImportAddress(t, err)
	if dialed {
		t.Fatal("dialer was called after DNS rebound to loopback")
	}
}

func TestImportDialPinsValidatedNumericAddress(t *testing.T) {
	policy, err := NewImportNetworkPolicy(nil)
	if err != nil {
		t.Fatal(err)
	}
	policy.resolver = importResolverFunc(func(
		_ context.Context,
		_ string,
		_ string,
	) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	})
	var dialedAddress string
	var peer net.Conn
	policy.dialer = importDialerFunc(func(
		_ context.Context,
		_ string,
		address string,
	) (net.Conn, error) {
		dialedAddress = address
		client, server := net.Pipe()
		peer = server
		return client, nil
	})
	ctx := WithImportNetworkPolicy(context.Background(), policy)
	connection, err := importDialContext(ctx, "tcp", "git.example:443")
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
	_ = peer.Close()
	if dialedAddress != "8.8.8.8:443" {
		t.Fatalf("dialed address = %q, want validated numeric address", dialedAddress)
	}
}

func TestImportRedirectPolicyRejectsPrivateTarget(t *testing.T) {
	request, err := http.NewRequest(
		http.MethodGet,
		"http://169.254.169.254/latest/meta-data",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	request = request.WithContext(
		WithImportNetworkPolicy(request.Context(), ImportNetworkPolicy{}),
	)
	err = importCheckRedirect(request, []*http.Request{{}})
	requireBlockedImportAddress(t, err)

	request.URL.Scheme = "file"
	err = importCheckRedirect(request, []*http.Request{{}})
	if err == nil || !strings.Contains(err.Error(), "HTTP or HTTPS") {
		t.Fatalf("non-HTTP redirect error = %v", err)
	}
}

func TestImportRedirectDoesNotForwardAuthorizationAcrossOrigins(t *testing.T) {
	request := httptest.NewRequest(
		http.MethodGet,
		"https://8.8.8.8/repository.git",
		nil,
	)
	request.Header.Set("Authorization", "Basic secret")
	request = request.WithContext(
		WithImportNetworkPolicy(request.Context(), ImportNetworkPolicy{}),
	)
	original := httptest.NewRequest(
		http.MethodGet,
		"https://1.1.1.1/repository.git",
		nil,
	)
	if err := importCheckRedirect(request, []*http.Request{original}); err != nil {
		t.Fatal(err)
	}
	if authorization := request.Header.Get("Authorization"); authorization != "" {
		t.Fatalf("redirect forwarded authorization: %q", authorization)
	}
}

func TestRemoteImportRejectsRedirectToPrivateAddress(t *testing.T) {
	var privateTargetReached atomic.Bool
	privateTarget := httptest.NewServer(http.HandlerFunc(func(
		http.ResponseWriter,
		*http.Request,
	) {
		privateTargetReached.Store(true)
	}))
	defer privateTarget.Close()

	redirect := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		http.Redirect(
			writer,
			request,
			privateTarget.URL+request.URL.RequestURI(),
			http.StatusFound,
		)
	}))
	defer redirect.Close()
	redirectURL, err := url.Parse(redirect.URL)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := NewImportNetworkPolicy([]string{"origin.example"})
	if err != nil {
		t.Fatal(err)
	}
	policy.resolver = importResolverFunc(func(
		_ context.Context,
		_ string,
		host string,
	) ([]netip.Addr, error) {
		if host != "origin.example" {
			return nil, errors.New("unexpected host")
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	})
	ctx := WithImportNetworkPolicy(context.Background(), policy)
	store := Store{Root: t.TempDir()}
	_, err = store.stageRemoteRepository(ctx, "source", ImportRepositoryOptions{
		URL: "http://origin.example:" + redirectURL.Port() + "/repository.git",
	})
	requireBlockedImportAddress(t, err)
	if privateTargetReached.Load() {
		t.Fatal("remote import followed a redirect to a private address")
	}
}
