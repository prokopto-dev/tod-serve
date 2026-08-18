package outbound

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The deny list, driven directly. docs/concepts/invariants.md names this test: a deny list only
// ever exercised through a dial that failed is a deny list whose holes somebody else finds.
func TestDenyReason_EveryDeniedAddress_IsRefused(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		addr   string
		reason string
	}{
		// The four the invariant names by category.
		{"loopback v4", "127.0.0.1", "loopback"},
		{"loopback v4 elsewhere in the block", "127.99.4.2", "loopback"},
		{"loopback v6", "::1", "loopback"},
		{"private 10/8", "10.0.0.7", "private"},
		{"private 172.16/12", "172.20.13.1", "private"},
		{"private 192.168/16", "192.168.1.1", "private"},
		{"unique local v6", "fd12:3456::1", "unique local"},
		{"link-local v4", "169.254.10.1", "link-local"},
		{"link-local v6", "fe80::1", "link-local"},

		// Cloud metadata, named individually. This is the half the invariant calls out
		// specifically, because a filter that denies only RFC 1918 lets every one of these through.
		{"AWS, Azure, GCP, DO metadata", "169.254.169.254", "cloud metadata"},
		{"AWS ECS task metadata", "169.254.170.2", "cloud metadata"},
		{"Alibaba Cloud metadata", "100.100.100.200", "cloud metadata"},
		{"Oracle Cloud metadata", "192.0.0.192", "cloud metadata"},
		{"AWS IPv6 IMDS", "fd00:ec2::254", "cloud metadata"},

		// The ranges that are neither private nor loopback and are still not somewhere to dial.
		{"this-network", "0.0.0.0", "this-network"},
		{"this-network non-zero", "0.1.2.3", "this-network"},
		{"unspecified v6", "::", "unspecified"},
		{"carrier-grade NAT", "100.70.1.1", "carrier-grade NAT"},
		{"IETF protocol assignments", "192.0.0.1", "IETF protocol assignments"},
		{"benchmarking", "198.19.0.1", "benchmarking"},
		{"documentation v4", "203.0.113.9", "documentation"},
		{"documentation v6", "2001:db8::1", "documentation"},
		{"multicast v4", "239.1.2.3", "multicast"},
		{"multicast v6", "ff02::1", "multicast"},
		{"reserved", "240.0.0.1", "reserved"},
		{"broadcast", "255.255.255.255", "reserved"},

		// The mapping that defeats most address filters, and the tunnels that would defeat the
		// rest. ::ffff:169.254.169.254 is the metadata service written so an IsPrivate check on
		// the v6 form says nothing at all.
		{"IPv4-mapped loopback", "::ffff:127.0.0.1", "loopback"},
		{"IPv4-mapped metadata", "::ffff:169.254.169.254", "cloud metadata"},
		{"IPv4-mapped private", "::ffff:10.1.2.3", "private"},
		{"NAT64 carrying metadata", "64:ff9b::a9fe:a9fe", "NAT64"},
		{"6to4 carrying metadata", "2002:a9fe:a9fe::1", "6to4"},
		{"Teredo", "2001:0:4136:e378:8000:63bf:3fff:fdd2", "Teredo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			addr, err := netip.ParseAddr(tt.addr)
			require.NoError(t, err)

			got := DenyReason(addr)
			require.NotEmpty(t, got, "%s must be denied", tt.addr)
			require.Contains(t, got, tt.reason)
		})
	}
}

// The other direction. A deny list that refuses everything passes the test above and makes the
// product unable to reach Discord, so the addresses that must still work are pinned too.
func TestDenyReason_PublicAddresses_AreAllowed(t *testing.T) {
	t.Parallel()

	for _, s := range []string{
		"1.1.1.1",
		"8.8.8.8",
		"162.159.128.233", // discord.com at time of writing
		"2606:4700:4700::1111",
		"2a00:1450:4009:81f::200e",
	} {
		t.Run(s, func(t *testing.T) {
			t.Parallel()
			addr, err := netip.ParseAddr(s)
			require.NoError(t, err)
			require.Empty(t, DenyReason(addr), "%s must be dialable", s)
		})
	}
}

func TestDenyReason_ZonedAndInvalidAddresses_AreRefused(t *testing.T) {
	t.Parallel()

	require.NotEmpty(t, DenyReason(netip.Addr{}), "the zero address must be refused")

	zoned, err := netip.ParseAddr("fe80::1%eth0")
	require.NoError(t, err)
	require.Contains(t, DenyReason(zoned), "zone")
}

// stubResolver answers with whatever the test wrote down, so the rebinding cases can be exercised
// without owning a domain whose DNS lies.
type stubResolver struct {
	addrs []netip.Addr
	calls int
}

func (r *stubResolver) LookupNetIP(_ context.Context, _, _ string) ([]netip.Addr, error) {
	r.calls++
	return r.addrs, nil
}

func mustAddrs(t *testing.T, ss ...string) []netip.Addr {
	t.Helper()
	out := make([]netip.Addr, 0, len(ss))
	for _, s := range ss {
		a, err := netip.ParseAddr(s)
		require.NoError(t, err)
		out = append(out, a)
	}
	return out
}

func newTestDialer(t *testing.T, r *stubResolver, host string) (*guardedDialer, *[]string) {
	t.Helper()
	dialed := &[]string{}
	return &guardedDialer{
		resolve: r,
		dial: func(_ context.Context, _, address string) (net.Conn, error) {
			*dialed = append(*dialed, address)
			return nil, errors.New("test dialer does not connect")
		},
		allow: hostSet{host: {}},
	}, dialed
}

func TestGuardedDialer_NameResolvingToADeniedAddress_IsRefused(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		addr string
	}{
		{"loopback", "127.0.0.1"},
		{"private", "10.0.0.1"},
		{"cloud metadata", "169.254.169.254"},
		{"IPv4-mapped metadata", "::ffff:169.254.169.254"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := &stubResolver{addrs: mustAddrs(t, tt.addr)}
			d, dialed := newTestDialer(t, r, "issuer.example")

			_, err := d.DialContext(t.Context(), "tcp", "issuer.example:443")

			require.ErrorIs(t, err, ErrRefused)
			require.Empty(t, *dialed, "no socket may be opened to a denied address")
		})
	}
}

// The rebinding case, and the reason the guard refuses the WHOLE name rather than filtering.
//
// An attacker's resolver answers with one address that passes and one that does not. Anything
// that picks the survivor is one retry away from connecting to the other, because the set it
// picked from is under the attacker's control and changes between queries.
func TestGuardedDialer_NameResolvingToOnePublicAndOnePrivateAddress_IsRefusedEntirely(t *testing.T) {
	t.Parallel()

	r := &stubResolver{addrs: mustAddrs(t, "93.184.216.34", "169.254.169.254")}
	d, dialed := newTestDialer(t, r, "issuer.example")

	_, err := d.DialContext(t.Context(), "tcp", "issuer.example:443")

	require.ErrorIs(t, err, ErrRefused)
	require.Empty(t, *dialed, "one denied answer refuses the whole name")
}

// The rebinding defence itself: resolve once, check, then dial the ADDRESS. If the guard handed
// the NAME down, a second lookup would happen inside the dial and the attacker's resolver would
// get a second chance to answer differently. This asserts there is no second lookup and no name.
func TestGuardedDialer_Dials_TheCheckedLiteralNeverTheName(t *testing.T) {
	t.Parallel()

	r := &stubResolver{addrs: mustAddrs(t, "93.184.216.34")}
	d, dialed := newTestDialer(t, r, "issuer.example")

	_, err := d.DialContext(t.Context(), "tcp", "issuer.example:443")

	require.Error(t, err, "the test dialer never connects")
	require.Equal(t, []string{"93.184.216.34:443"}, *dialed)
	require.Equal(t, 1, r.calls, "the name is resolved exactly once, so a rebind has no second answer to win")
}

func TestGuardedDialer_AddressLiteral_IsCheckedWithoutAResolver(t *testing.T) {
	t.Parallel()

	r := &stubResolver{}
	d, dialed := newTestDialer(t, r, "169.254.169.254")

	_, err := d.DialContext(t.Context(), "tcp", "169.254.169.254:80")

	require.ErrorIs(t, err, ErrRefused)
	require.Zero(t, r.calls, "a literal never reaches the resolver")
	require.Empty(t, *dialed)
}

func TestGuardedDialer_HostOutsideTheAllowlist_IsRefused(t *testing.T) {
	t.Parallel()

	r := &stubResolver{addrs: mustAddrs(t, "93.184.216.34")}
	d, dialed := newTestDialer(t, r, "issuer.example")

	_, err := d.DialContext(t.Context(), "tcp", "elsewhere.example:443")

	require.ErrorIs(t, err, ErrHostNotAllowed)
	require.Zero(t, r.calls)
	require.Empty(t, *dialed)
}

// The end-to-end proof, through New rather than through a hand-built dialer: a real client, a
// real listener, and the guard between them. The server is on 127.0.0.1, which is denied, so a
// client that reached it would be a client whose guard is not wired in.
func TestClient_LoopbackServer_IsUnreachable(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	host, _, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	require.NoError(t, err)

	c, err := New(Policy{AllowHosts: []string{host}})
	require.NoError(t, err)

	_, err = c.Do(t.Context(), http.MethodGet, "https://"+host+"/", nil, nil)

	require.ErrorIs(t, err, ErrRefused)
	require.Contains(t, err.Error(), "loopback")
}

func TestNew_PolicyWithNoAllowedHosts_IsRefused(t *testing.T) {
	t.Parallel()

	_, err := New(Policy{})
	require.Error(t, err)

	_, err = New(Policy{AllowHosts: []string{"  "}})
	require.Error(t, err)
}

// roundTripFunc stands in for the network so the response-handling half — redirects, the size cap
// — can be exercised without one. The dialer half is covered above, against a real socket.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// newStubbedClient builds a real client through New, then replaces only the transport. Everything
// New decided — the redirect policy, the size cap, the allowlist — is still the code under test.
func newStubbedClient(t *testing.T, p Policy, rt roundTripFunc) *Client {
	t.Helper()
	c, err := New(p)
	require.NoError(t, err)
	c.http.Transport = rt
	return c
}

func TestClient_Redirect_IsNotFollowed(t *testing.T) {
	t.Parallel()

	var hops int
	c := newStubbedClient(t, Policy{AllowHosts: []string{"issuer.example"}},
		func(r *http.Request) (*http.Response, error) {
			hops++
			return &http.Response{
				StatusCode: http.StatusFound,
				Header:     http.Header{"Location": []string{"https://169.254.169.254/latest/meta-data/"}},
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    r,
			}, nil
		})

	_, err := c.Do(t.Context(), http.MethodGet, "https://issuer.example/jwks", nil, nil)

	require.ErrorIs(t, err, ErrRedirect)
	require.Equal(t, 1, hops, "a redirect is refused, never followed onto the endpoint's choice of host")
}

func TestClient_ResponseOverTheCap_IsRefused(t *testing.T) {
	t.Parallel()

	body := strings.Repeat("a", 1025)
	c := newStubbedClient(t, Policy{AllowHosts: []string{"issuer.example"}, MaxResponseBytes: 1024},
		func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{},
				Body:       io.NopCloser(strings.NewReader(body)),
				Request:    r,
			}, nil
		})

	_, err := c.Do(t.Context(), http.MethodGet, "https://issuer.example/jwks", nil, nil)

	require.ErrorIs(t, err, ErrTooLarge)
}

func TestClient_ResponseExactlyAtTheCap_IsAccepted(t *testing.T) {
	t.Parallel()

	body := strings.Repeat("a", 1024)
	c := newStubbedClient(t, Policy{AllowHosts: []string{"issuer.example"}, MaxResponseBytes: 1024},
		func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{},
				Body:       io.NopCloser(strings.NewReader(body)),
				Request:    r,
			}, nil
		})

	got, err := c.Do(t.Context(), http.MethodGet, "https://issuer.example/jwks", nil, nil)

	require.NoError(t, err)
	require.Len(t, got.Body, 1024)
}

func TestClient_NonHTTPSAndDisallowedHosts_AreRefusedBeforeASocket(t *testing.T) {
	t.Parallel()

	var attempts int
	c := newStubbedClient(t, Policy{AllowHosts: []string{"issuer.example"}},
		func(r *http.Request) (*http.Response, error) {
			attempts++
			return nil, errors.New("must not be reached")
		})

	_, err := c.Do(t.Context(), http.MethodGet, "http://issuer.example/jwks", nil, nil)
	require.ErrorIs(t, err, ErrRefused)

	_, err = c.Do(t.Context(), http.MethodGet, "https://evil.example/jwks", nil, nil)
	require.ErrorIs(t, err, ErrHostNotAllowed)

	// The allowlist is exact. A suffix match would admit both of these, which is how an
	// allowlist becomes a suggestion.
	_, err = c.Do(t.Context(), http.MethodGet, "https://evil-issuer.example/jwks", nil, nil)
	require.ErrorIs(t, err, ErrHostNotAllowed)

	_, err = c.Do(t.Context(), http.MethodGet, "https://sub.issuer.example/jwks", nil, nil)
	require.ErrorIs(t, err, ErrHostNotAllowed)

	require.Zero(t, attempts)
}

// control is the assertion that the address handed to connect(2) is still the one that was
// checked. It cannot fire while DialContext dials a literal, which is exactly why it is here: it
// is where a future "just pass the hostname through" change fails a test instead of shipping.
func TestControl_DeniedAddress_IsRefusedAtConnectTime(t *testing.T) {
	t.Parallel()

	require.Error(t, control("tcp", "169.254.169.254:80", nil))
	require.NoError(t, control("tcp", "93.184.216.34:443", nil))
	require.Error(t, control("tcp", "not-an-address", nil))
}
