// Package vdd drives the SudoVDA indirect display driver on Windows: it adds
// a virtual monitor per client at the client's resolution and refresh rate,
// binds its rendering to a chosen GPU, and removes it again.
//
// The driver removes every monitor when nobody has sent it an IOCTL for its
// watchdog timeout (3 s by default). A Driver pings while it owns displays,
// so a host that dies in any way loses its displays within that timeout.
// Apollo uses the same driver: its pings keep Planck's displays alive and its
// render-adapter choice moves them, so the two should not run at once.
package vdd

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/planckdotgg/planck/pkg/gpu"
)

var (
	ErrNotInstalled  = errors.New("vdd: SudoVDA driver not installed")
	ErrAccessDenied  = errors.New("vdd: SudoVDA device denies this user")
	ErrProtocol      = errors.New("vdd: unsupported SudoVDA protocol")
	ErrAttachTimeout = errors.New("vdd: virtual display did not attach")
	ErrWrongAdapter  = errors.New("vdd: virtual display rendered on another GPU")
	ErrClosed        = errors.New("vdd: driver closed")
)

// GUID has the memory layout of a Windows GUID.
type GUID struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

func (g GUID) String() string {
	return fmt.Sprintf("{%08x-%04x-%04x-%02x%02x-%02x%02x%02x%02x%02x%02x}",
		g.Data1, g.Data2, g.Data3, g.Data4[0], g.Data4[1],
		g.Data4[2], g.Data4[3], g.Data4[4], g.Data4[5], g.Data4[6], g.Data4[7])
}

// MonitorID derives a stable monitor GUID from a client identity, so Windows
// remembers that monitor's scaling and HDR settings per client.
func MonitorID(seed []byte) GUID {
	h := sha256.New()
	h.Write([]byte("planck-vdd-v1"))
	h.Write(seed)
	b := h.Sum(nil)[:16]
	b[6] = b[6]&0x0f | 0x50
	b[8] = b[8]&0x3f | 0x80
	var g GUID
	g.Data1 = binary.BigEndian.Uint32(b[0:4])
	g.Data2 = binary.BigEndian.Uint16(b[4:6])
	g.Data3 = binary.BigEndian.Uint16(b[6:8])
	copy(g.Data4[:], b[8:16])
	return g
}

type Version struct {
	Major, Minor, Incremental uint8
	Test                      bool
}

func (v Version) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Incremental)
	if v.Test {
		s += "-test"
	}
	return s
}

type Info struct {
	DevicePath      string
	Protocol        Version
	Watchdog        time.Duration // 0 = disabled in the registry
	RegistryGPUName string        // HKLM\SOFTWARE\SudoMaker\SudoVDA\gpuName, read only
	MaxMonitors     int           // registry maxMonitors, default 10
}

type DisplaySpec struct {
	Width, Height  int  // even
	RefreshMilliHz int  // 120000
	ID             GUID // stable per client: MonitorID(clientFingerprint)
	Name           string
	Serial         string
	RenderAdapter  gpu.LUID // 0 = leave the driver's choice
	HDR            bool     // turn Windows advanced color on (true) or off (false)
	// ScalePct is the Windows display scale to set, in percent: the step
	// nearest it (100 is unscaled). 0 leaves Windows's choice, which for
	// SudoVDA's small reported size is often 200%: games in borderless
	// windows then see half the display.
	ScalePct      int
	Primary       bool
	AttachTimeout time.Duration // default 5 s
}

const (
	maxNameLen         = 13
	defaultName        = "Planck"
	defaultAttach      = 5 * time.Second
	wrongAdapterGrace  = 2 * time.Second
	attachPollInterval = 50 * time.Millisecond
)

// normalize fills defaults and rejects specs the driver would take but
// Windows could not show.
func (s DisplaySpec) normalize() (DisplaySpec, error) {
	if s.Width <= 0 || s.Height <= 0 || s.Width%2 != 0 || s.Height%2 != 0 {
		return s, fmt.Errorf("vdd: size %dx%d must be positive and even", s.Width, s.Height)
	}
	if s.RefreshMilliHz < 1000 {
		return s, fmt.Errorf("vdd: refresh %d mHz is below 1 Hz", s.RefreshMilliHz)
	}
	if s.ID == (GUID{}) {
		return s, errors.New("vdd: display spec has no ID")
	}
	if s.Name == "" {
		s.Name = defaultName
	}
	if s.Serial == "" {
		s.Serial = fmt.Sprintf("%08x%04x%04x", s.ID.Data1, s.ID.Data2, s.ID.Data3)[:maxNameLen]
	}
	for _, f := range []string{s.Name, s.Serial} {
		if len(f) > maxNameLen {
			return s, fmt.Errorf("vdd: %q is longer than %d bytes", f, maxNameLen)
		}
		for i := 0; i < len(f); i++ {
			if f[i] < 0x20 || f[i] > 0x7e {
				return s, fmt.Errorf("vdd: %q is not printable ASCII", f)
			}
		}
	}
	if s.AttachTimeout <= 0 {
		s.AttachTimeout = defaultAttach
	}
	return s, nil
}

type VirtualDisplay struct {
	ID             GUID
	IddAdapter     gpu.LUID // SudoVDA's IddCx adapter (from ADD)
	TargetID       uint32
	GDIName        string   // \\.\DISPLAYn
	RenderAdapter  gpu.LUID // the DXGI adapter whose output this is, verified
	Width, Height  int
	RefreshMilliHz int
	HDR            bool
	// ScalePct is the Windows display scale in effect, in percent; 0 when
	// the spec left it alone. ScaleErr says why it is not the spec's.
	ScalePct int
	ScaleErr error

	drv  *Driver
	spec DisplaySpec
}

// Monitor is a SudoVDA monitor that Windows shows on the desktop, whoever
// added it.
type Monitor struct {
	IddAdapter gpu.LUID
	TargetID   uint32
	GDIName    string
	Name       string
}

// ElevatedGPUNameCommand is the one-line PowerShell command a user runs from
// an elevated prompt to make SudoVDA render on adapterName by default. It
// restarts the driver, which removes every SudoVDA display.
func ElevatedGPUNameCommand(adapterName string) string {
	return fmt.Sprintf(`New-ItemProperty -Path 'HKLM:\SOFTWARE\SudoMaker\SudoVDA' -Name gpuName -PropertyType String -Value '%s' -Force | Out-Null; Get-PnpDevice -FriendlyName 'SudoMaker Virtual Display Adapter' | ForEach-Object { Disable-PnpDevice -InstanceId $_.InstanceId -Confirm:$false; Enable-PnpDevice -InstanceId $_.InstanceId -Confirm:$false }`,
		strings.ReplaceAll(adapterName, "'", "''"))
}

// dpiSteps are the display scales Windows offers, in percent. The
// DisplayConfig DPI calls count in steps of this table from the scale
// Windows recommends for the display.
var dpiSteps = [...]int{100, 125, 150, 175, 200, 225, 250, 300, 350, 400, 450, 500}

// dpiRelative is the step nearest pct, relative to the recommended scale,
// within [minRel, maxRel]. minRel is the step of 100%, so -minRel is the
// recommended step's index.
func dpiRelative(pct, minRel, maxRel int) int {
	best := 0
	for i, s := range dpiSteps {
		if abs(s-pct) < abs(dpiSteps[best]-pct) {
			best = i
		}
	}
	return min(max(best+minRel, minRel), maxRel)
}

// dpiPercent is the scale rel steps from the recommended one.
func dpiPercent(minRel, rel int) int {
	return dpiSteps[min(max(rel-minRel, 0), len(dpiSteps)-1)]
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
