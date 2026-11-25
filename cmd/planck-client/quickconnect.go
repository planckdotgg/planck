package main

import (
	"errors"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/planckdotgg/planck/internal/ui"
	"github.com/planckdotgg/planck/pkg/tailscale"
)

// quickKind is what Quick Connect text names.
type quickKind int

const (
	// quickAddress is an IP, or a host and port, to dial as typed.
	quickAddress quickKind = iota
	// quickName is a host's name or alias, or a Tailscale MagicDNS name.
	quickName
	// quickFingerprint is a host certificate's SHA-256, hex.
	quickFingerprint
	// quickShareLink is a planck.gg/j/ share link; text is its code.
	quickShareLink
)

// quickTarget is Quick Connect text, read: text is the address, the name,
// the lowercase fingerprint or the share code.
type quickTarget struct {
	kind quickKind
	text string
}

var errQuickEmpty = errors.New("type an address, a host's name, its fingerprint or a share link")

// parseQuickConnect reads what was typed in Quick Connect, trying in turn a
// share link, a fingerprint (64 hex digits, spaces, colons and dashes
// between groups allowed), an IP or host:port, and last a name.
func parseQuickConnect(s string) (quickTarget, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return quickTarget{}, errQuickEmpty
	}
	if code, ok := shareCode(s); ok {
		if code == "" || strings.Trim(code, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_") != "" {
			return quickTarget{}, errors.New("that share link has no code after planck.gg/j/")
		}
		return quickTarget{quickShareLink, code}, nil
	}
	if fp := strings.NewReplacer(" ", "", ":", "", "-", "").Replace(s); len(fp) == 64 && isHex(fp) {
		return quickTarget{quickFingerprint, strings.ToLower(fp)}, nil
	}
	if _, err := netip.ParseAddr(strings.Trim(s, "[]")); err == nil {
		return quickTarget{quickAddress, s}, nil
	}
	if host, port, err := net.SplitHostPort(s); err == nil && host != "" {
		if n, err := strconv.ParseUint(port, 10, 16); err != nil || n == 0 {
			return quickTarget{}, errors.New("the port after the colon must be 1 to 65535")
		}
		return quickTarget{quickAddress, s}, nil
	}
	if strings.ContainsAny(s, " /\\@") {
		return quickTarget{}, errors.New("that is not an address, a host's name, a fingerprint or a share link")
	}
	return quickTarget{quickName, strings.TrimSuffix(s, ".")}, nil
}

// shareCode is the code of a planck.gg/j/ link, with or without its
// scheme, and whether s is one.
func shareCode(s string) (string, bool) {
	for _, prefix := range []string{"https://", "http://", "www."} {
		s = trimPrefixFold(s, prefix)
	}
	rest := trimPrefixFold(s, "planck.gg/j/")
	if len(rest) == len(s) {
		return "", false
	}
	return strings.TrimSuffix(rest, "/"), true
}

func trimPrefixFold(s, prefix string) string {
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return s[len(prefix):]
	}
	return s
}

func isHex(s string) bool {
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}

// quickConnect turns what Connect was given into what resolveTarget
// takes: a host card's ID, or an address. A name matches a host's name or
// alias, then a tailnet peer's MagicDNS or machine name; a name with a dot
// that matches neither is dialed as a DNS name.
func (b *backend) quickConnect(target string) (string, error) {
	target = strings.TrimSpace(target)
	hosts := b.Hosts()
	if slices.ContainsFunc(hosts, func(h ui.HostInfo) bool { return h.ID == target }) {
		return target, nil
	}
	q, err := parseQuickConnect(target)
	if err != nil {
		return "", err
	}
	switch q.kind {
	case quickShareLink:
		return "", ui.ErrShareLink
	case quickAddress:
		return q.text, nil
	case quickFingerprint:
		for _, h := range hosts {
			if h.Fingerprint == q.text {
				return h.ID, nil
			}
		}
		return "", ui.ErrUnknownHost
	}
	for _, h := range hosts {
		if strings.EqualFold(h.Name, q.text) || strings.EqualFold(h.AdvertisedName, q.text) {
			return h.ID, nil
		}
	}
	if ip, ok := magicDNS(b.finder.tailnetStatus(), q.text); ok {
		return withDefaultPort(ip.String()), nil
	}
	if strings.Contains(q.text, ".") {
		return q.text, nil
	}
	return "", ui.ErrUnknownHost
}

// magicDNS finds the tailnet peer called name, by its MagicDNS name, its
// first label, or its machine name, and returns its first IPv4 address,
// else its first address.
func magicDNS(st *tailscale.Status, name string) (netip.Addr, bool) {
	if st == nil {
		return netip.Addr{}, false
	}
	for _, p := range st.Peers {
		dns := strings.TrimSuffix(p.DNSName, ".")
		label, _, _ := strings.Cut(dns, ".")
		if len(p.TailscaleIPs) == 0 || !(strings.EqualFold(dns, name) || strings.EqualFold(label, name) || strings.EqualFold(p.HostName, name)) {
			continue
		}
		for _, ip := range p.TailscaleIPs {
			if ip.Is4() {
				return ip, true
			}
		}
		return p.TailscaleIPs[0], true
	}
	return netip.Addr{}, false
}
