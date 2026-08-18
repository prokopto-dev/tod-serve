package outbound

import (
	"errors"
	"fmt"
	"net/netip"
)

// The deny list, as prefixes. Each entry names the reason it is refused, because a dial that
// failed with "denied" tells an operator debugging their OIDC issuer nothing about which rule
// caught it, and the reason is the difference between "fix your DNS" and "you cannot point this
// at your metadata service".
//
// The list is written as prefixes rather than as calls to netip.Addr's IsPrivate/IsLoopback
// helpers wherever the range has a NAME worth reporting. The helpers still run underneath, so a
// range nobody wrote down is refused rather than allowed.
var denied = []struct {
	prefix netip.Prefix
	reason string
}{
	// Cloud metadata first, so the message says "cloud metadata" rather than "link-local". Every
	// one of these is inside a range denied below anyway; naming them is what makes the invariant
	// "denies cloud-metadata addresses, not merely RFC1918" a thing a reader can check.
	{netip.MustParsePrefix("169.254.169.254/32"), "cloud metadata (AWS, Azure, GCP, DigitalOcean IMDS)"},
	{netip.MustParsePrefix("169.254.170.2/32"), "cloud metadata (AWS ECS task metadata)"},
	{netip.MustParsePrefix("100.100.100.200/32"), "cloud metadata (Alibaba Cloud)"},
	{netip.MustParsePrefix("192.0.0.192/32"), "cloud metadata (Oracle Cloud)"},
	{netip.MustParsePrefix("fd00:ec2::254/128"), "cloud metadata (AWS IPv6 IMDS)"},

	{netip.MustParsePrefix("0.0.0.0/8"), "this-network (RFC 1122)"},
	{netip.MustParsePrefix("10.0.0.0/8"), "private (RFC 1918)"},
	{netip.MustParsePrefix("100.64.0.0/10"), "carrier-grade NAT (RFC 6598)"},
	{netip.MustParsePrefix("127.0.0.0/8"), "loopback"},
	{netip.MustParsePrefix("169.254.0.0/16"), "link-local (RFC 3927)"},
	{netip.MustParsePrefix("172.16.0.0/12"), "private (RFC 1918)"},
	{netip.MustParsePrefix("192.0.0.0/24"), "IETF protocol assignments (RFC 6890)"},
	{netip.MustParsePrefix("192.0.2.0/24"), "documentation (RFC 5737)"},
	{netip.MustParsePrefix("192.168.0.0/16"), "private (RFC 1918)"},
	{netip.MustParsePrefix("198.18.0.0/15"), "benchmarking (RFC 2544)"},
	{netip.MustParsePrefix("198.51.100.0/24"), "documentation (RFC 5737)"},
	{netip.MustParsePrefix("203.0.113.0/24"), "documentation (RFC 5737)"},
	{netip.MustParsePrefix("224.0.0.0/4"), "multicast"},
	{netip.MustParsePrefix("240.0.0.0/4"), "reserved (RFC 1112), including the broadcast address"},

	{netip.MustParsePrefix("::/128"), "unspecified"},
	{netip.MustParsePrefix("::1/128"), "loopback"},
	{netip.MustParsePrefix("100::/64"), "discard-only (RFC 6666)"},
	{netip.MustParsePrefix("2001:db8::/32"), "documentation (RFC 3849)"},
	{netip.MustParsePrefix("fc00::/7"), "unique local (RFC 4193)"},
	{netip.MustParsePrefix("fe80::/10"), "link-local"},
	{netip.MustParsePrefix("ff00::/8"), "multicast"},
}

// Tunnels that carry an IPv4 destination inside an IPv6 address. Each is refused outright rather
// than unwrapped and re-checked: the embedded address is attacker-chosen, the unwrapping rules
// differ per tunnel, and nothing this product talks to is reachable only over one of them. A
// refusal that is occasionally too broad is the correct trade against a decoder bug that lets
// 2002:a9fe:a9fe:: reach the metadata service.
var tunnels = []struct {
	prefix netip.Prefix
	reason string
}{
	{netip.MustParsePrefix("64:ff9b::/96"), "NAT64 tunnel carrying an IPv4 destination (RFC 6052)"},
	{netip.MustParsePrefix("64:ff9b:1::/48"), "local-use NAT64 tunnel (RFC 8215)"},
	{netip.MustParsePrefix("2001::/32"), "Teredo tunnel carrying an IPv4 destination (RFC 4380)"},
	{netip.MustParsePrefix("2002::/16"), "6to4 tunnel carrying an IPv4 destination (RFC 3056)"},
}

// DenyReason reports why addr must not be dialled, or the empty string if it may be.
//
// It is the whole deny list, exported so it can be unit-tested directly — docs/concepts/invariants.md
// names that test, because a deny list only ever exercised through a failed dial is a deny list
// whose holes are found by somebody else.
//
// An IPv4-mapped IPv6 address (::ffff:169.254.169.254) is unwrapped and checked as the IPv4
// address it is. That mapping is the single most common way an address-based filter is defeated,
// so it is handled first and pinned by its own test case.
func DenyReason(addr netip.Addr) string {
	if !addr.IsValid() {
		return "not an IP address"
	}
	if addr.Zone() != "" {
		// A zone means a link-scoped address — fe80::1%eth0. Nothing reachable over the public
		// internet needs one, and it is a way to name an interface rather than a host.
		return "carries an interface zone, which is a link-scoped address"
	}

	// Unmap BEFORE anything else. ::ffff:127.0.0.1 is loopback, and an IsLoopback check on the
	// v6 form of some runtimes says otherwise.
	if addr.Is4In6() {
		addr = addr.Unmap()
	}

	for _, t := range tunnels {
		if t.prefix.Contains(addr) {
			return t.reason
		}
	}
	for _, d := range denied {
		if d.prefix.Contains(addr) {
			return d.reason
		}
	}

	// The prefixes above are the ranges worth naming. These catch anything the list forgot: a
	// range added to the internet's reserved set after this was written is refused rather than
	// dialled, which is the direction a security default has to fail in.
	switch {
	case addr.IsLoopback():
		return "loopback"
	case addr.IsUnspecified():
		return "unspecified"
	case addr.IsPrivate():
		return "private"
	case addr.IsLinkLocalUnicast():
		return "link-local"
	case addr.IsLinkLocalMulticast():
		return "link-local multicast"
	case addr.IsInterfaceLocalMulticast():
		return "interface-local multicast"
	case addr.IsMulticast():
		return "multicast"
	case !addr.IsGlobalUnicast():
		return "not global unicast"
	}
	return ""
}

// ErrRefused is what every guard in this package unwraps to, so a caller can ask "was this
// refused by policy, or did the network fail?" with errors.Is and get an answer. The distinction
// matters at the edge: a refusal is a misconfigured provider (the operator's problem) and a
// network failure is identity_provider_unreachable (nobody's).
var ErrRefused = errors.New("outbound request refused by policy")

// ErrDenied is the deny list refusing one resolved address. It unwraps to [ErrRefused]; the
// reason is in the message, because it is for a human reading a log rather than for a branch.
type ErrDenied struct {
	Host   string
	Addr   netip.Addr
	Reason string
}

func (e *ErrDenied) Error() string {
	return fmt.Sprintf("dial %s: it resolves to %s, which is %s", e.Host, e.Addr, e.Reason)
}

// Unwrap reports the sentinel, so errors.Is(err, ErrRefused) holds.
func (e *ErrDenied) Unwrap() error { return ErrRefused }
