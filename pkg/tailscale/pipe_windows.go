package tailscale

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const defaultPipe = `\\.\pipe\ProtectedPrefix\Administrators\Tailscale\tailscaled`

func platformEndpoint() (endpoint, error) {
	return endpoint{network: "pipe", address: defaultPipe}, nil
}

const (
	errorPipeBusy syscall.Errno = 231
	errorMoreData syscall.Errno = 234

	fileFlagOverlapped     = 0x40000000
	securitySQOSPresent    = 0x00100000
	securityIdentification = 0x00010000
)

var (
	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	procCreateEventW        = kernel32.NewProc("CreateEventW")
	procGetOverlappedResult = kernel32.NewProc("GetOverlappedResult")
)

// dialPipe opens the named pipe for overlapped I/O. Overlapped mode matters:
// on a synchronous handle a pending ReadFile blocks every WriteFile, and a
// blocked read cannot be cancelled when the context ends. The identification
// impersonation level is what tailscaled needs to learn the caller's user.
func dialPipe(ctx context.Context, path string) (net.Conn, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	for {
		h, err := syscall.CreateFile(name,
			syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil,
			syscall.OPEN_EXISTING,
			fileFlagOverlapped|securitySQOSPresent|securityIdentification, 0)
		if err == nil {
			return &pipeConn{h: h, path: path}, nil
		}
		if err != errorPipeBusy {
			return nil, &os.PathError{Op: "open", Path: path, Err: err}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

type pipeConn struct {
	h    syscall.Handle
	path string

	mu       sync.Mutex
	inflight int
	closed   bool
}

func (c *pipeConn) begin() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return net.ErrClosed
	}
	c.inflight++
	return nil
}

func (c *pipeConn) end() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inflight--
	if c.closed && c.inflight == 0 {
		syscall.CloseHandle(c.h)
	}
}

// Close cancels pending I/O; the handle is released when the last
// in-flight operation returns, so no operation ever sees a reused handle.
func (c *pipeConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	if c.inflight == 0 {
		return syscall.CloseHandle(c.h)
	}
	syscall.CancelIoEx(c.h, nil)
	return nil
}

func (c *pipeConn) io(b []byte, write bool) (int, error) {
	if err := c.begin(); err != nil {
		return 0, err
	}
	defer c.end()
	ev, _, callErr := procCreateEventW.Call(0, 1, 0, 0)
	if ev == 0 {
		return 0, fmt.Errorf("CreateEventW: %w", callErr)
	}
	defer syscall.CloseHandle(syscall.Handle(ev))
	ov := &syscall.Overlapped{HEvent: syscall.Handle(ev)}
	var n uint32
	if write {
		err := syscall.WriteFile(c.h, b, &n, ov)
		if err != nil && err != syscall.ERROR_IO_PENDING {
			return 0, err
		}
	} else {
		err := syscall.ReadFile(c.h, b, &n, ov)
		if err != nil && err != syscall.ERROR_IO_PENDING && err != errorMoreData {
			return 0, err
		}
	}
	ok, _, callErr := procGetOverlappedResult.Call(uintptr(c.h), uintptr(unsafe.Pointer(ov)), uintptr(unsafe.Pointer(&n)), 1)
	runtime.KeepAlive(ov)
	runtime.KeepAlive(b)
	if ok == 0 && callErr != errorMoreData {
		return int(n), callErr
	}
	return int(n), nil
}

func (c *pipeConn) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	n, err := c.io(b, false)
	switch {
	case errors.Is(err, syscall.ERROR_BROKEN_PIPE):
		return n, io.EOF
	case errors.Is(err, syscall.ERROR_OPERATION_ABORTED):
		return n, net.ErrClosed
	case err == nil && n == 0:
		return 0, io.EOF
	}
	return n, err
}

func (c *pipeConn) Write(b []byte) (int, error) {
	total := 0
	for total < len(b) {
		n, err := c.io(b[total:], true)
		total += n
		if err != nil {
			if errors.Is(err, syscall.ERROR_OPERATION_ABORTED) {
				err = net.ErrClosed
			}
			return total, err
		}
	}
	return total, nil
}

type pipeAddr string

func (a pipeAddr) Network() string { return "pipe" }
func (a pipeAddr) String() string  { return string(a) }

func (c *pipeConn) LocalAddr() net.Addr  { return pipeAddr(c.path) }
func (c *pipeConn) RemoteAddr() net.Addr { return pipeAddr(c.path) }

// Deadlines are not supported; callers bound requests with a context, which
// closes the conn and cancels pending I/O.
func (c *pipeConn) SetDeadline(time.Time) error      { return nil }
func (c *pipeConn) SetReadDeadline(time.Time) error  { return nil }
func (c *pipeConn) SetWriteDeadline(time.Time) error { return nil }
