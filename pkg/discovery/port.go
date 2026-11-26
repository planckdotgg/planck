package discovery

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"time"
)

// yieldEvery bounds how often a port with no host on it hands the port's
// unicast traffic to another process (yield).
const yieldEvery = 5 * time.Second

// PacketWriter is the sending half of a net.PacketConn, which a Port is
// too.
type PacketWriter interface {
	WriteTo(p []byte, addr net.Addr) (int, error)
}

// Port is a discovery socket shared by the hosts and the browsers of one
// process: OpenPort gives every caller of an address the same socket, so
// that an app hosting and browsing at once does not bind its port twice.
// One reader answers probes with each host's beacon (Serve) and hands
// beacons to each browser (Listen).
//
// The socket lets other processes bind the port as well. macOS and Windows
// deliver a broadcast to every socket on the port but a unicast datagram
// to the oldest one alone, so a port with no host on it that hears a probe
// binds its socket anew, at most once per yieldEvery, leaving unicast
// probes to a host in another process.
type Port struct {
	addr string
	// key is the address in ports, "" for a port not shared.
	key    string
	done   chan struct{}
	exited chan struct{}

	mu         sync.Mutex
	conn       net.PacketConn
	refs       int
	next       int
	responders map[int]func() Beacon
	listeners  map[int]func(Announcement)
	lastYield  time.Time
}

var ports = struct {
	sync.Mutex
	m map[string]*Port
}{m: map[string]*Port{}}

// OpenPort binds addr, such as ":41000", or joins the socket this process
// already has on it. Each OpenPort needs its own Close. An address on port
// 0 binds a socket of its own each time.
func OpenPort(addr string) (*Port, error) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if port == "0" || port == "" {
		return newPort(addr, "")
	}
	ports.Lock()
	defer ports.Unlock()
	if p := ports.m[addr]; p != nil {
		p.mu.Lock()
		p.refs++
		p.mu.Unlock()
		return p, nil
	}
	p, err := newPort(addr, addr)
	if err != nil {
		return nil, err
	}
	ports.m[addr] = p
	return p, nil
}

func newPort(addr, key string) (*Port, error) {
	conn, err := listenShared(addr)
	if err != nil {
		return nil, err
	}
	p := &Port{addr: addr, key: key, conn: conn, refs: 1, done: make(chan struct{}), exited: make(chan struct{}),
		responders: map[int]func() Beacon{}, listeners: map[int]func(Announcement){}}
	go p.read()
	return p, nil
}

func listenShared(addr string) (net.PacketConn, error) {
	lc := net.ListenConfig{Control: shareAddr}
	return lc.ListenPacket(context.Background(), "udp4", addr)
}

// Close lets go of the port; the last Close closes the socket and waits
// for its reader to end.
func (p *Port) Close() error {
	if p.key != "" {
		ports.Lock()
		defer ports.Unlock()
	}
	p.mu.Lock()
	p.refs--
	if p.refs > 0 {
		p.mu.Unlock()
		return nil
	}
	if p.key != "" {
		delete(ports.m, p.key)
	}
	close(p.done)
	var err error
	if p.conn != nil {
		err = p.conn.Close()
	}
	p.mu.Unlock()
	<-p.exited
	return err
}

// LocalAddr is the socket's address.
func (p *Port) LocalAddr() net.Addr {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn == nil {
		return nil
	}
	return p.conn.LocalAddr()
}

// WriteTo sends from the port's socket, as Announce does.
func (p *Port) WriteTo(b []byte, addr net.Addr) (int, error) {
	p.mu.Lock()
	conn := p.conn
	p.mu.Unlock()
	if conn == nil {
		return 0, net.ErrClosed
	}
	return conn.WriteTo(b, addr)
}

// Serve answers probes with beacon, read at each reply, until ctx ends.
func (p *Port) Serve(ctx context.Context, beacon func() Beacon) error {
	return p.attach(ctx, func(id int) { p.responders[id] = beacon }, func(id int) { delete(p.responders, id) })
}

// Listen reports the beacons the port hears until ctx ends.
func (p *Port) Listen(ctx context.Context, found func(Announcement)) error {
	return p.attach(ctx, func(id int) { p.listeners[id] = found }, func(id int) { delete(p.listeners, id) })
}

func (p *Port) attach(ctx context.Context, add, remove func(id int)) error {
	p.mu.Lock()
	p.next++
	id := p.next
	add(id)
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		remove(id)
		p.mu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		return net.ErrClosed
	}
}

func (p *Port) read() {
	defer close(p.exited)
	buf := make([]byte, maxMessageLen+1)
	for {
		p.mu.Lock()
		conn := p.conn
		p.mu.Unlock()
		if conn == nil {
			// A yield could not bind again; try once in a while.
			select {
			case <-p.done:
				return
			case <-time.After(time.Second):
			}
			p.rebind()
			continue
		}
		n, from, err := conn.ReadFrom(buf)
		if err != nil {
			select {
			case <-p.done:
				return
			default:
			}
			// Windows reports an ICMP port-unreachable for an earlier reply
			// as a read error on the next ReadFrom; the socket is still
			// usable. A socket closed by a yield has been replaced.
			continue
		}
		p.dispatch(conn, buf[:n], from)
	}
}

func (p *Port) dispatch(conn net.PacketConn, b []byte, from net.Addr) {
	m, err := decode(b)
	if err != nil {
		return
	}
	switch m.Type {
	case msgProbe:
		p.mu.Lock()
		beacons := make([]func() Beacon, 0, len(p.responders))
		for _, fn := range p.responders {
			beacons = append(beacons, fn)
		}
		p.mu.Unlock()
		if len(beacons) == 0 {
			if _, own := ownProbes.Load(m.Nonce); !own {
				p.yield()
			}
			return
		}
		for _, fn := range beacons {
			bc := fn()
			reply, err := encode(message{Type: msgReply, Nonce: m.Nonce, Beacon: &bc})
			if err == nil {
				conn.WriteTo(reply, from)
			}
		}
	case msgBeacon:
		ua, ok := from.(*net.UDPAddr)
		if !ok {
			return
		}
		ap := ua.AddrPort()
		a := Announcement{Beacon: *m.Beacon, Addr: netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port()), Kind: PathLANDirect}
		p.mu.Lock()
		found := make([]func(Announcement), 0, len(p.listeners))
		for _, fn := range p.listeners {
			found = append(found, fn)
		}
		p.mu.Unlock()
		for _, fn := range found {
			fn(a)
		}
	}
}

// yield binds the socket anew, which makes it the newest on the port, so
// that unicast probes reach the oldest: a host in another process, if any.
// It runs on the reader.
func (p *Port) yield() {
	if p.key == "" {
		return
	}
	p.mu.Lock()
	if time.Since(p.lastYield) < yieldEvery {
		p.mu.Unlock()
		return
	}
	p.lastYield = time.Now()
	p.mu.Unlock()
	p.rebind()
}

func (p *Port) rebind() {
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-p.done:
		return
	default:
	}
	if p.conn != nil {
		p.conn.Close()
		p.conn = nil
	}
	conn, err := listenShared(p.addr)
	if err != nil {
		return
	}
	p.conn = conn
}
