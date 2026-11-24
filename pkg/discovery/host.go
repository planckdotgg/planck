package discovery

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"time"
)

// Serve answers probes on conn with the host's beacon until ctx ends. Replies
// go to the probe's source address, so conn must not be connected.
func Serve(ctx context.Context, conn net.PacketConn, b Beacon) error {
	return ServeFunc(ctx, conn, func() Beacon { return b })
}

// ServeFunc is Serve with the beacon read at each reply, so a renamed host
// answers with its new name at once.
func ServeFunc(ctx context.Context, conn net.PacketConn, beacon func() Beacon) error {
	stop := context.AfterFunc(ctx, func() { conn.SetReadDeadline(time.Unix(1, 0)) })
	defer stop()
	buf := make([]byte, maxMessageLen+1)
	for {
		n, from, err := conn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, net.ErrClosed) {
				return err
			}
			// Windows reports an ICMP port-unreachable for an earlier reply as
			// a read error on the next ReadFrom; the socket is still usable.
			continue
		}
		m, err := decode(buf[:n])
		if err != nil || m.Type != msgProbe {
			continue
		}
		b := beacon()
		reply, err := encode(message{Type: msgReply, Nonce: m.Nonce, Beacon: &b})
		if err != nil {
			return err
		}
		conn.WriteTo(reply, from)
	}
}

// Announce sends the beacon to every address in dst now and then once per
// interval until ctx ends. Send errors on one address do not stop the others;
// interfaces come and go.
func Announce(ctx context.Context, conn net.PacketConn, b Beacon, dst []netip.AddrPort, interval time.Duration) error {
	return AnnounceFunc(ctx, conn, func() Beacon { return b }, dst, interval)
}

// AnnounceFunc is Announce with the beacon read before each round.
func AnnounceFunc(ctx context.Context, conn net.PacketConn, beacon func() Beacon, dst []netip.AddrPort, interval time.Duration) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		b := beacon()
		pkt, err := encode(message{Type: msgBeacon, Beacon: &b})
		if err != nil {
			return err
		}
		for _, d := range dst {
			conn.WriteTo(pkt, net.UDPAddrFromAddrPort(d))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// BroadcastAddrs returns the directed broadcast address of every up IPv4
// interface that supports broadcast. The limited broadcast 255.255.255.255
// leaves through only one interface on macOS, which misses clients on the
// other NIC of a multi-homed host.
func BroadcastAddrs(port uint16) []netip.AddrPort {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []netip.AddrPort
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagBroadcast == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			p, ok := netipPrefix(ipn)
			if !ok || !p.Addr().Is4() || p.Bits() >= 31 {
				continue
			}
			out = append(out, netip.AddrPortFrom(directedBroadcast(p), port))
		}
	}
	return out
}

func netipPrefix(n *net.IPNet) (netip.Prefix, bool) {
	a, ok := netip.AddrFromSlice(n.IP)
	if !ok {
		return netip.Prefix{}, false
	}
	bits, _ := n.Mask.Size()
	a = a.Unmap()
	if a.Is4() && len(n.Mask) == net.IPv6len {
		bits -= 96
	}
	return netip.PrefixFrom(a, bits), true
}

func directedBroadcast(p netip.Prefix) netip.Addr {
	b := p.Addr().As4()
	host := uint32(1)<<(32-p.Bits()) - 1
	v := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	v |= host
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}
