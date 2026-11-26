//go:build linux && !android && (amd64 || arm64)

package wl

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"github.com/ebitengine/purego"
)

// Proxy is a struct wl_proxy*: a protocol object.
type Proxy uintptr

// Interface is a struct wl_interface: libwayland's own for the core
// protocol (Core), or one defined here in Go memory, which must outlive
// every object made with it (the package's are never freed).
type Interface struct {
	c    *cInterface
	name string
}

// cInterface and cMessage are struct wl_interface and struct wl_message.
type cInterface struct {
	name        *byte
	version     int32
	methodCount int32
	methods     *cMessage
	eventCount  int32
	_           int32
	events      *cMessage
}

type cMessage struct {
	name, signature *byte
	types           **cInterface
}

// Message is a request or event of an Interface: its name, libwayland's
// signature string, and the interface of each object or new_id argument
// (nil for the others).
type Message struct {
	Name, Signature string
	Types           []*Interface
}

// Name is the interface's protocol name.
func (i *Interface) Name() string { return i.name }

func (i *Interface) ptr() uintptr { return uintptr(unsafe.Pointer(i.c)) }

var lib struct {
	once sync.Once
	err  error

	marshalArrayFlags, addDispatcher, proxyDestroy, proxyGetVersion, proxyGetID uintptr
	createWrapper, wrapperDestroy, proxySetQueue                                uintptr
	createQueue, destroyQueue, roundtripQueue, dispatchQueuePending             uintptr
	prepareReadQueue, cancelRead, readEvents, flush, getFD, getError            uintptr

	dispatcher uintptr
	core       map[string]*Interface
}

// Load loads libwayland-client and defines the package's interfaces. It
// is safe to call more than once.
func Load() error {
	lib.once.Do(func() { lib.err = load() })
	return lib.err
}

func load() error {
	h, err := purego.Dlopen("libwayland-client.so.0", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return fmt.Errorf("wl: %w", err)
	}
	for _, f := range []struct {
		p    *uintptr
		name string
	}{
		{&lib.marshalArrayFlags, "wl_proxy_marshal_array_flags"},
		{&lib.addDispatcher, "wl_proxy_add_dispatcher"},
		{&lib.proxyDestroy, "wl_proxy_destroy"},
		{&lib.proxyGetVersion, "wl_proxy_get_version"},
		{&lib.proxyGetID, "wl_proxy_get_id"},
		{&lib.createWrapper, "wl_proxy_create_wrapper"},
		{&lib.wrapperDestroy, "wl_proxy_wrapper_destroy"},
		{&lib.proxySetQueue, "wl_proxy_set_queue"},
		{&lib.createQueue, "wl_display_create_queue"},
		{&lib.destroyQueue, "wl_event_queue_destroy"},
		{&lib.roundtripQueue, "wl_display_roundtrip_queue"},
		{&lib.dispatchQueuePending, "wl_display_dispatch_queue_pending"},
		{&lib.prepareReadQueue, "wl_display_prepare_read_queue"},
		{&lib.cancelRead, "wl_display_cancel_read"},
		{&lib.readEvents, "wl_display_read_events"},
		{&lib.flush, "wl_display_flush"},
		{&lib.getFD, "wl_display_get_fd"},
		{&lib.getError, "wl_display_get_error"},
	} {
		if *f.p, err = purego.Dlsym(h, f.name); err != nil {
			return fmt.Errorf("wl: libwayland-client lacks %s (it needs 1.20 or later): %w", f.name, err)
		}
	}
	lib.core = map[string]*Interface{}
	for _, name := range coreNames {
		sym, err := purego.Dlsym(h, name+"_interface")
		if err != nil {
			return fmt.Errorf("wl: %w", err)
		}
		lib.core[name] = &Interface{c: (*cInterface)(ptr(sym)), name: name}
	}
	lib.dispatcher = purego.NewCallback(dispatch)
	defineProtocols()
	return nil
}

var coreNames = []string{
	"wl_display", "wl_registry", "wl_callback", "wl_compositor", "wl_subcompositor", "wl_subsurface",
	"wl_surface", "wl_region", "wl_buffer", "wl_shm", "wl_shm_pool", "wl_seat", "wl_pointer",
	"wl_keyboard", "wl_output",
}

// Core is libwayland's own interface of the core protocol, as
// "wl_surface". Load must have succeeded.
func Core(name string) *Interface {
	i := lib.core[name]
	if i == nil {
		panic("wl: no core interface " + name)
	}
	return i
}

// ptr turns a C address into a pointer without go vet's unsafeptr
// complaint; the memory is C's or kept alive elsewhere.
func ptr(a uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&a)) }

// keep holds the C strings and tables of defined interfaces.
var keep [][]byte

func cstr(s string) *byte {
	b := append([]byte(s), 0)
	keep = append(keep, b)
	return &b[0]
}

// define fills i, allocated before so that interfaces can name each other
// in any order.
func define(i *Interface, name string, version int, requests, events []Message) {
	i.name = name
	i.c = &cInterface{name: cstr(name), version: int32(version)}
	msgs := func(ms []Message) *cMessage {
		if len(ms) == 0 {
			return nil
		}
		out := make([]cMessage, len(ms))
		for k, m := range ms {
			out[k] = cMessage{name: cstr(m.Name), signature: cstr(m.Signature)}
			if len(m.Types) > 0 {
				types := make([]*cInterface, len(m.Types))
				for t, ti := range m.Types {
					if ti != nil {
						types[t] = ti.c
					}
				}
				out[k].types = &types[0]
				keepTypes = append(keepTypes, types)
			}
		}
		keepMsgs = append(keepMsgs, out)
		return &out[0]
	}
	i.c.methodCount, i.c.methods = int32(len(requests)), msgs(requests)
	i.c.eventCount, i.c.events = int32(len(events)), msgs(events)
}

var (
	keepTypes [][]*cInterface
	keepMsgs  [][]cMessage
)

// Fixed is a wl_fixed_t from a float.
func Fixed(v float64) uintptr { return uintptr(uint32(int32(v * 256))) }

// Int passes a signed argument.
func Int(v int) uintptr { return uintptr(uint32(int32(v))) }

// marshalArgs is the argument array of every request; marshalMu keeps
// one request at a time on it. A package variable never moves, as a
// goroutine's stack may between taking an address and the C call.
var (
	marshalMu   sync.Mutex
	marshalArgs [12]uint64
)

const marshalFlagDestroy = 1

// Marshal sends request opcode of p with args (at most 12: uintptr
// values, Fixed, Int, a C string's address, a Proxy). A request that makes
// an object takes iface and version for it and a 0 in its new_id's place,
// and returns the new proxy; for others iface is nil and it returns 0.
//
//go:uintptrescapes
func (p Proxy) Marshal(opcode uint32, iface *Interface, version uint32, args ...uintptr) Proxy {
	return p.marshal(opcode, iface, version, 0, args)
}

// Destroy sends p's destructor request opcode (most interfaces' 0) and
// frees p.
func (p Proxy) Destroy(opcode uint32, args ...uintptr) {
	if p == 0 {
		return
	}
	forget(p)
	p.marshal(opcode, nil, p.Version(), marshalFlagDestroy, args)
}

// Free frees p without a request, for objects with no destructor
// (wl_callback, wp_presentation_feedback) once their last event came.
func (p Proxy) Free() {
	if p == 0 {
		return
	}
	forget(p)
	purego.SyscallN(lib.proxyDestroy, uintptr(p))
}

func (p Proxy) marshal(opcode uint32, iface *Interface, version, flags uint32, args []uintptr) Proxy {
	var ip uintptr
	if iface != nil {
		ip = iface.ptr()
	}
	marshalMu.Lock()
	for i, a := range args {
		marshalArgs[i] = uint64(a)
	}
	r, _, _ := purego.SyscallN(lib.marshalArrayFlags, uintptr(p), uintptr(opcode), ip, uintptr(version), uintptr(flags), uintptr(unsafe.Pointer(&marshalArgs[0])))
	marshalMu.Unlock()
	return Proxy(r)
}

// Version is the version p was made with.
func (p Proxy) Version() uint32 {
	r, _, _ := purego.SyscallN(lib.proxyGetVersion, uintptr(p))
	return uint32(r)
}

// ID is p's protocol object id.
func (p Proxy) ID() uint32 {
	r, _, _ := purego.SyscallN(lib.proxyGetID, uintptr(p))
	return uint32(r)
}

// Event holds the arguments of an event, as union wl_argument.
type Event struct{ args uintptr }

func (e Event) at(i int) uint64 { return *(*uint64)(ptr(e.args + uintptr(i)*8)) }

func (e Event) Uint(i int) uint32 { return uint32(e.at(i)) }
func (e Event) Int(i int) int32   { return int32(uint32(e.at(i))) }

// Fixed is a wl_fixed_t argument as a float.
func (e Event) Fixed(i int) float64 { return float64(int32(uint32(e.at(i)))) / 256 }

// Proxy is an object or new_id argument.
func (e Event) Proxy(i int) Proxy { return Proxy(uintptr(e.at(i))) }

// FD is an fd argument, which the handler owns and must close.
func (e Event) FD(i int) int { return int(int32(uint32(e.at(i)))) }

// String is a string argument.
func (e Event) String(i int) string {
	p := uintptr(e.at(i))
	if p == 0 {
		return ""
	}
	var b []byte
	for ; *(*byte)(ptr(p)) != 0; p++ {
		b = append(b, *(*byte)(ptr(p)))
	}
	return string(b)
}

// Array is an array argument's bytes, valid during the handler.
func (e Event) Array(i int) []byte {
	a := uintptr(e.at(i)) // struct wl_array { size_t size, alloc; void *data; }
	if a == 0 {
		return nil
	}
	size := *(*uintptr)(ptr(a))
	data := *(*uintptr)(ptr(a + 16))
	if size == 0 || data == 0 {
		return nil
	}
	return unsafe.Slice((*byte)(ptr(data)), size)
}

// Handler takes p's events, on the goroutine that dispatches its queue.
type Handler func(opcode uint32, e Event)

var handlers struct {
	sync.Mutex
	byProxy map[Proxy]Handler
}

// Listen sets h to take p's events. A proxy takes one handler, once.
func (p Proxy) Listen(h Handler) error {
	handlers.Lock()
	if handlers.byProxy == nil {
		handlers.byProxy = map[Proxy]Handler{}
	}
	handlers.byProxy[p] = h
	handlers.Unlock()
	// The implementation pointer is only handed back; p itself is unique.
	if r, _, _ := purego.SyscallN(lib.addDispatcher, uintptr(p), lib.dispatcher, uintptr(p), 0); int32(r) != 0 {
		forget(p)
		return errors.New("wl: the object has a listener already")
	}
	return nil
}

func forget(p Proxy) {
	handlers.Lock()
	delete(handlers.byProxy, p)
	handlers.Unlock()
}

// dispatch is libwayland's wl_dispatcher_func_t.
func dispatch(impl, proxy, opcode, msg, args uintptr) uintptr {
	handlers.Lock()
	h := handlers.byProxy[Proxy(proxy)]
	handlers.Unlock()
	if h != nil {
		h(uint32(opcode), Event{args: args})
	}
	return 0
}

// Global is a global the compositor announced.
type Global struct {
	Name      uint32
	Interface string
	Version   uint32
}

// Queue is a private event queue on a display connection, with a registry
// on it.
type Queue struct {
	display, queue, wrapper uintptr
	registry                Proxy

	mu      sync.Mutex
	globals []Global

	stop    [2]int
	running chan struct{}
}

// NewQueue makes a queue on display, a struct wl_display* opened by
// someone else, and reads its globals.
func NewQueue(display uintptr) (*Queue, error) {
	if err := Load(); err != nil {
		return nil, err
	}
	if display == 0 {
		return nil, errors.New("wl: no display")
	}
	q := &Queue{display: display, stop: [2]int{-1, -1}}
	q.queue, _, _ = purego.SyscallN(lib.createQueue, display)
	if q.queue == 0 {
		return nil, errors.New("wl: wl_display_create_queue failed")
	}
	q.wrapper, _, _ = purego.SyscallN(lib.createWrapper, display)
	if q.wrapper == 0 {
		purego.SyscallN(lib.destroyQueue, q.queue)
		return nil, errors.New("wl: wl_proxy_create_wrapper failed")
	}
	purego.SyscallN(lib.proxySetQueue, q.wrapper, q.queue)
	q.registry = Proxy(q.wrapper).Marshal(1, Core("wl_registry"), Proxy(q.wrapper).Version(), 0) // wl_display.get_registry
	if q.registry == 0 {
		q.Close()
		return nil, errors.New("wl: wl_display.get_registry failed")
	}
	q.registry.Listen(func(op uint32, e Event) {
		if op != 0 { // global_remove
			return
		}
		q.mu.Lock()
		q.globals = append(q.globals, Global{Name: e.Uint(0), Interface: e.String(1), Version: e.Uint(2)})
		q.mu.Unlock()
	})
	if err := q.Roundtrip(); err != nil {
		q.Close()
		return nil, err
	}
	return q, nil
}

// Globals lists the globals announced so far.
func (q *Queue) Globals() []Global {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]Global(nil), q.globals...)
}

// Bind binds the first global of iface at the lower of its version and
// most, or returns 0 when the compositor has none.
func (q *Queue) Bind(iface *Interface, most uint32) (Proxy, uint32) {
	q.mu.Lock()
	var g Global
	for _, c := range q.globals {
		if c.Interface == iface.name {
			g = c
			break
		}
	}
	q.mu.Unlock()
	if g.Interface == "" {
		return 0, 0
	}
	v := min(g.Version, most)
	name := append([]byte(iface.name), 0)
	p := q.registry.Marshal(0, iface, v, uintptr(g.Name), uintptr(unsafe.Pointer(&name[0])), uintptr(v), 0)
	runtime.KeepAlive(name)
	return p, v
}

// Roundtrip sends what is buffered and dispatches the queue until the
// compositor answered it all.
func (q *Queue) Roundtrip() error {
	if r, _, _ := purego.SyscallN(lib.roundtripQueue, q.display, q.queue); int32(r) < 0 {
		return q.lost()
	}
	return nil
}

// Flush sends what is buffered.
func (q *Queue) Flush() { purego.SyscallN(lib.flush, q.display) }

func (q *Queue) lost() error {
	code, _, _ := purego.SyscallN(lib.getError, q.display)
	return fmt.Errorf("wl: the display connection failed (error %d)", int32(code))
}

// Run dispatches the queue's events on the calling goroutine until Close.
func (q *Queue) Run() error {
	q.mu.Lock()
	if q.running != nil {
		q.mu.Unlock()
		return errors.New("wl: the queue runs already")
	}
	var p [2]int
	if err := syscall.Pipe2(p[:], syscall.O_CLOEXEC|syscall.O_NONBLOCK); err != nil {
		q.mu.Unlock()
		return err
	}
	q.stop = p
	q.running = make(chan struct{})
	done := q.running
	q.mu.Unlock()
	defer close(done)
	fd, _, _ := purego.SyscallN(lib.getFD, q.display)
	fds := [2]pollFD{{fd: int32(fd), events: pollIn}, {fd: int32(p[0]), events: pollIn}}
	for {
		for {
			r, _, _ := purego.SyscallN(lib.prepareReadQueue, q.display, q.queue)
			if int32(r) == 0 {
				break
			}
			purego.SyscallN(lib.dispatchQueuePending, q.display, q.queue)
		}
		q.Flush()
		fds[0].revents, fds[1].revents = 0, 0
		_, _, errno := syscall.Syscall6(syscall.SYS_PPOLL, uintptr(unsafe.Pointer(&fds[0])), 2, 0, 0, 0, 0)
		if fds[1].revents != 0 {
			purego.SyscallN(lib.cancelRead, q.display)
			return nil
		}
		if errno != 0 || fds[0].revents&pollIn == 0 {
			purego.SyscallN(lib.cancelRead, q.display)
			if errno == syscall.EINTR {
				continue
			}
			if fds[0].revents&(pollErr|pollHup) != 0 {
				return q.lost()
			}
			continue
		}
		if r, _, _ := purego.SyscallN(lib.readEvents, q.display); int32(r) < 0 {
			return q.lost()
		}
		if r, _, _ := purego.SyscallN(lib.dispatchQueuePending, q.display, q.queue); int32(r) < 0 {
			return q.lost()
		}
	}
}

type pollFD struct {
	fd              int32
	events, revents int16
}

const (
	pollIn  = 0x1
	pollErr = 0x8
	pollHup = 0x10
)

// Close stops Run, if it runs, and frees the queue and its registry. The
// caller destroys its own objects on the queue first.
func (q *Queue) Close() {
	q.mu.Lock()
	running := q.running
	stop := q.stop
	q.mu.Unlock()
	if running != nil {
		syscall.Write(stop[1], []byte{0})
		<-running
		syscall.Close(stop[0])
		syscall.Close(stop[1])
	}
	if q.registry != 0 {
		forget(q.registry)
		purego.SyscallN(lib.proxyDestroy, uintptr(q.registry))
		q.registry = 0
	}
	if q.wrapper != 0 {
		purego.SyscallN(lib.wrapperDestroy, q.wrapper)
		q.wrapper = 0
	}
	if q.queue != 0 {
		q.Flush()
		purego.SyscallN(lib.destroyQueue, q.queue)
		q.queue = 0
	}
}
