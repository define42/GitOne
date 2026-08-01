package storage

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

type importNetworkPolicyContextKey struct{}

type importIPResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type importNetworkDialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

// ImportNetworkPolicy controls which network destinations remote repository
// imports may contact. Its zero value blocks non-public destinations.
type ImportNetworkPolicy struct {
	allowedHosts    map[string]struct{}
	allowedPrefixes []netip.Prefix
	resolver        importIPResolver
	dialer          importNetworkDialer
}

// ImportAddressError reports a remote import destination rejected by policy.
type ImportAddressError struct {
	Host   string
	Reason string
}

func (e *ImportAddressError) Error() string {
	return fmt.Sprintf("remote import host %q is blocked: %s", e.Host, e.Reason)
}

// NewImportNetworkPolicy creates the default-deny policy plus explicit
// administrator exceptions. Entries are exact hostnames, IP addresses, or
// CIDR prefixes.
func NewImportNetworkPolicy(allowlist []string) (ImportNetworkPolicy, error) {
	policy := ImportNetworkPolicy{
		allowedHosts: make(map[string]struct{}),
	}
	for _, raw := range allowlist {
		entry := normalizeImportHost(raw)
		if entry == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(entry); err == nil {
			policy.allowedPrefixes = append(
				policy.allowedPrefixes,
				prefix.Masked(),
			)
			continue
		}
		if address, err := netip.ParseAddr(entry); err == nil {
			address = address.Unmap()
			bits := 128
			if address.Is4() {
				bits = 32
			}
			policy.allowedPrefixes = append(
				policy.allowedPrefixes,
				netip.PrefixFrom(address, bits),
			)
			continue
		}
		if err := validateImportAllowlistHostname(entry); err != nil {
			return ImportNetworkPolicy{}, err
		}
		policy.allowedHosts[entry] = struct{}{}
	}
	return policy, nil
}

func validateImportAllowlistHostname(host string) error {
	if strings.ContainsAny(host, " \t\r\n/@[]:") ||
		strings.HasPrefix(host, ".") ||
		strings.Contains(host, "..") ||
		len(host) > 253 {
		return fmt.Errorf("invalid remote import allowlist entry %q", host)
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 ||
			len(label) > 63 ||
			label[0] == '-' ||
			label[len(label)-1] == '-' {
			return fmt.Errorf("invalid remote import allowlist entry %q", host)
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') &&
				(character < '0' || character > '9') &&
				character != '-' {
				return fmt.Errorf("invalid remote import allowlist entry %q", host)
			}
		}
	}
	parsed, err := url.Parse("http://" + host)
	if err != nil || parsed.Hostname() != host || parsed.Port() != "" {
		return fmt.Errorf("invalid remote import allowlist entry %q", host)
	}
	return nil
}

func normalizeImportHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

func (p ImportNetworkPolicy) resolverOrDefault() importIPResolver {
	if p.resolver != nil {
		return p.resolver
	}
	return net.DefaultResolver
}

func (p ImportNetworkPolicy) dialerOrDefault() importNetworkDialer {
	if p.dialer != nil {
		return p.dialer
	}
	// Replacing DialContext on the cloned DefaultTransport drops the 30s dial
	// timeout it would otherwise apply, so set one explicitly. Without it, an
	// import pointed at a routable-but-unresponsive address would pin a
	// goroutine, socket, and staging directory until the OS-level TCP timeout.
	return &net.Dialer{Timeout: 30 * time.Second}
}

func (p ImportNetworkPolicy) hostExplicitlyAllowed(host string) bool {
	_, ok := p.allowedHosts[normalizeImportHost(host)]
	return ok
}

func (p ImportNetworkPolicy) addressExplicitlyAllowed(address netip.Addr) bool {
	address = address.Unmap()
	for _, prefix := range p.allowedPrefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func blockedImportAddress(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsValid() ||
		address.IsUnspecified() ||
		address.IsLoopback() ||
		address.IsLinkLocalUnicast() ||
		address.IsLinkLocalMulticast() ||
		address.IsPrivate() ||
		address.IsMulticast() ||
		!address.IsGlobalUnicast() {
		return true
	}
	for _, prefix := range [...]netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("169.254.0.0/16"),
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.0.0.0/24"),
		netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("192.168.0.0/16"),
		netip.MustParsePrefix("192.88.99.0/24"),
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("203.0.113.0/24"),
		netip.MustParsePrefix("224.0.0.0/4"),
		netip.MustParsePrefix("240.0.0.0/4"),
		netip.MustParsePrefix("::/128"),
		netip.MustParsePrefix("::1/128"),
		netip.MustParsePrefix("64:ff9b::/96"),
		netip.MustParsePrefix("64:ff9b:1::/48"),
		netip.MustParsePrefix("100::/64"),
		netip.MustParsePrefix("2001::/32"),
		netip.MustParsePrefix("2001:2::/48"),
		netip.MustParsePrefix("2001:db8::/32"),
		netip.MustParsePrefix("2002::/16"),
		netip.MustParsePrefix("fc00::/7"),
		netip.MustParsePrefix("fe80::/10"),
		netip.MustParsePrefix("ff00::/8"),
	} {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func (p ImportNetworkPolicy) resolveAllowed(
	ctx context.Context,
	host string,
) ([]netip.Addr, error) {
	normalizedHost := normalizeImportHost(host)
	if normalizedHost == "" {
		return nil, &ImportAddressError{Host: host, Reason: "host is empty"}
	}
	var addresses []netip.Addr
	if literal, err := netip.ParseAddr(normalizedHost); err == nil {
		addresses = []netip.Addr{literal}
	} else {
		resolved, resolveErr := p.resolverOrDefault().LookupNetIP(
			ctx,
			"ip",
			normalizedHost,
		)
		if resolveErr != nil {
			return nil, fmt.Errorf("resolve remote import host %q: %w", normalizedHost, resolveErr)
		}
		addresses = resolved
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("remote import host %q resolved to no addresses", normalizedHost)
	}
	hostAllowed := p.hostExplicitlyAllowed(normalizedHost)
	allowed := make([]netip.Addr, 0, len(addresses))
	for _, address := range addresses {
		address = address.Unmap()
		if !hostAllowed &&
			!p.addressExplicitlyAllowed(address) &&
			blockedImportAddress(address) {
			return nil, &ImportAddressError{
				Host:   normalizedHost,
				Reason: "it resolves to a non-public network address",
			}
		}
		allowed = append(allowed, address)
	}
	return allowed, nil
}

// ValidateURL resolves and validates one HTTP(S) import destination.
func (p ImportNetworkPolicy) ValidateURL(ctx context.Context, rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Hostname() == "" {
		return errors.New("remote import URL has no host")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("remote import URL must use HTTP or HTTPS")
	}
	_, err = p.resolveAllowed(ctx, parsed.Hostname())
	return err
}

// WithImportNetworkPolicy applies a policy to the import-only HTTP transport.
func WithImportNetworkPolicy(
	ctx context.Context,
	policy ImportNetworkPolicy,
) context.Context {
	return context.WithValue(ctx, importNetworkPolicyContextKey{}, policy)
}

func importNetworkPolicyFromContext(ctx context.Context) ImportNetworkPolicy {
	policy, _ := ctx.Value(importNetworkPolicyContextKey{}).(ImportNetworkPolicy)
	return policy
}

func importDialContext(
	ctx context.Context,
	network string,
	address string,
) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("parse remote import address: %w", err)
	}
	policy := importNetworkPolicyFromContext(ctx)
	addresses, err := policy.resolveAllowed(ctx, host)
	if err != nil {
		return nil, err
	}
	var dialErrors []error
	for _, candidate := range addresses {
		connection, dialErr := policy.dialerOrDefault().DialContext(
			ctx,
			network,
			net.JoinHostPort(candidate.String(), port),
		)
		if dialErr == nil {
			return connection, nil
		}
		dialErrors = append(dialErrors, dialErr)
	}
	return nil, errors.Join(dialErrors...)
}

func importCheckRedirect(request *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("remote import has too many redirects")
	}
	policy := importNetworkPolicyFromContext(request.Context())
	if err := policy.ValidateURL(request.Context(), request.URL.String()); err != nil {
		return fmt.Errorf("remote import redirect rejected: %w", err)
	}
	if len(via) > 0 && via[0].URL != nil &&
		!sameImportURLOrigin(via[0].URL, request.URL) {
		request.Header.Del("Authorization")
	}
	return nil
}

func sameImportURLOrigin(left, right *url.URL) bool {
	return strings.EqualFold(left.Scheme, right.Scheme) &&
		strings.EqualFold(left.Hostname(), right.Hostname()) &&
		effectiveImportURLPort(left) == effectiveImportURLPort(right)
}

func effectiveImportURLPort(value *url.URL) string {
	if port := value.Port(); port != "" {
		return port
	}
	if value.Scheme == "https" {
		return "443"
	}
	return "80"
}

func newImportHTTPClient() *http.Client {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		panic("http.DefaultTransport is not *http.Transport")
	}
	safeTransport := base.Clone()
	safeTransport.Proxy = nil
	safeTransport.DialContext = importDialContext
	// SSRF filtering depends on every connection flowing through
	// importDialContext, so ensure TLS is dialed through it too rather than a
	// separate path, and bound how long a stalled server can hold the request.
	safeTransport.DialTLSContext = nil
	safeTransport.ResponseHeaderTimeout = 60 * time.Second
	return &http.Client{
		Transport:     safeTransport,
		CheckRedirect: importCheckRedirect,
	}
}
