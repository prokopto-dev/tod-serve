package outbound

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"syscall"
)

// resolver is the DNS lookup the guard performs. It is an interface so a test can hand the dialer
// a name that resolves to 127.0.0.1 without owning a domain, which is the only honest way to test
// the rebinding path.
type resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// dialFunc opens the connection once the guard has decided. It is a field rather than a direct
// call to net.Dialer so that TestGuardedDialer_Dials_TheCheckedLiteralNeverTheName can read the
// address the guard actually handed down — the assertion that the rebinding defence is real.
type dialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// guardedDialer resolves a name once, checks every address it got, and then dials the ADDRESS.
type guardedDialer struct {
	resolve resolver
	dial    dialFunc
	allow   hostSet
}

// DialContext is where the SSRF guard actually sits.
//
// The ordering is resolve → check → dial-the-literal, and the last hyphenated word is the part
// that matters. The obvious implementation — look the name up, decide it is safe, then hand the
// NAME to net.Dial — has a window between the check and the connection in which the attacker's
// resolver can answer a second query differently. That is DNS rebinding, and it defeats a filter
// that validated a hostname rather than a socket.
//
// Here the name is resolved exactly once, every address that came back is checked, and the dial
// is issued to a netip.Addr literal. There is no second lookup for a rebind to win, because there
// is no second lookup at all.
//
// Two smaller decisions, both deliberate:
//
//   - If ANY resolved address is denied the whole dial is refused, rather than the denied ones
//     being filtered out and a surviving public address used. A name answering with one public
//     and one loopback address is a rebinding attempt dressed as a multi-homed host, and there is
//     no legitimate OIDC issuer that needs us to guess which half was meant.
//   - The dialer's Control hook re-checks the address the kernel is about to connect to. Dialing
//     a literal means it can only ever see the address already checked — which is the point: it
//     is a standing assertion that this stays true, and it is where a future "just pass the
//     hostname through, it is simpler" change fails a test instead of shipping.
func (d *guardedDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("split dial address %s: %w", address, err)
	}
	if err := d.allow.check(host); err != nil {
		return nil, err
	}

	addrs, err := d.addresses(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("resolve %s: no addresses: %w", host, ErrRefused)
	}
	for _, a := range addrs {
		if reason := DenyReason(a); reason != "" {
			return nil, &ErrDenied{Host: host, Addr: a, Reason: reason}
		}
	}

	portNum, err := net.LookupPort(network, port)
	if err != nil {
		return nil, fmt.Errorf("parse port %s: %w", port, err)
	}

	var lastErr error
	for _, a := range addrs {
		target := netip.AddrPortFrom(a.Unmap(), uint16(portNum))
		conn, err := d.dial(ctx, network, target.String())
		if err != nil {
			lastErr = err
			continue
		}
		return conn, nil
	}
	return nil, fmt.Errorf("dial %s: %w", host, lastErr)
}

// addresses resolves host, or parses it when it is already an address literal. A literal is
// checked by the same deny list rather than trusted: https://127.0.0.1/ is the shortest SSRF
// there is, and it never touches a resolver.
func (d *guardedDialer) addresses(ctx context.Context, host string) ([]netip.Addr, error) {
	if a, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{a}, nil
	}
	addrs, err := d.resolve.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", host, err)
	}
	return addrs, nil
}

// control re-checks the address the socket is about to connect to, immediately before connect(2).
// See [guardedDialer.DialContext] for why this is an assertion rather than the guard.
func control(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("parse connect address %s: %w", address, err)
	}
	if reason := DenyReason(ap.Addr()); reason != "" {
		return &ErrDenied{Host: address, Addr: ap.Addr(), Reason: reason}
	}
	return nil
}
