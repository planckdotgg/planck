//go:build windows

package vdd

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/planckdotgg/planck/pkg/gpu"
)

var (
	user32                          = windows.NewLazySystemDLL("user32.dll")
	procGetDisplayConfigBufferSizes = user32.NewProc("GetDisplayConfigBufferSizes")
	procQueryDisplayConfig          = user32.NewProc("QueryDisplayConfig")
	procSetDisplayConfig            = user32.NewProc("SetDisplayConfig")
	procDisplayConfigGetDeviceInfo  = user32.NewProc("DisplayConfigGetDeviceInfo")
	procDisplayConfigSetDeviceInfo  = user32.NewProc("DisplayConfigSetDeviceInfo")
	procEnumDisplaySettingsW        = user32.NewProc("EnumDisplaySettingsW")
	procChangeDisplaySettingsExW    = user32.NewProc("ChangeDisplaySettingsExW")
)

const (
	qdcOnlyActivePaths = 0x2

	sdcApply                  = 0x80
	sdcUseSuppliedDisplayConf = 0x20
	sdcSaveToDatabase         = 0x200
	sdcAllowChanges           = 0x400

	infoGetSourceName         = 1
	infoGetTargetName         = 2
	infoGetAdapterName        = 4
	infoGetAdvancedColorInfo  = 9
	infoSetAdvancedColorState = 10
	// The DPI scale calls are undocumented; Windows's display settings use
	// them (DISPLAYCONFIG_DEVICE_INFO_GET/SET_DPI_SCALE, -3 and -4).
	infoGetDPIScale = 0xfffffffd
	infoSetDPIScale = 0xfffffffc

	modeInfoTypeSource = 1
	modeIdxInvalid     = 0xffffffff
)

type pathSourceInfo struct {
	AdapterID   windows.LUID
	ID          uint32
	ModeInfoIdx uint32
	StatusFlags uint32
}

type pathTargetInfo struct {
	AdapterID        windows.LUID
	ID               uint32
	ModeInfoIdx      uint32
	OutputTechnology uint32
	Rotation         uint32
	Scaling          uint32
	RefreshNum       uint32
	RefreshDen       uint32
	ScanLineOrdering uint32
	TargetAvailable  int32
	StatusFlags      uint32
}

type pathInfo struct { // DISPLAYCONFIG_PATH_INFO, 72 bytes
	Source pathSourceInfo
	Target pathTargetInfo
	Flags  uint32
}

type modeInfo struct { // DISPLAYCONFIG_MODE_INFO, 64 bytes
	InfoType  uint32
	ID        uint32
	AdapterID windows.LUID
	// For a source mode: Width, Height, PixelFormat, PositionX, PositionY.
	Union [12]uint32
}

type deviceInfoHeader struct { // DISPLAYCONFIG_DEVICE_INFO_HEADER, 20 bytes
	Type      uint32
	Size      uint32
	AdapterID windows.LUID
	ID        uint32
}

type sourceDeviceName struct { // 84 bytes
	Header        deviceInfoHeader
	GDIDeviceName [32]uint16
}

type targetDeviceName struct { // DISPLAYCONFIG_TARGET_DEVICE_NAME, 420 bytes
	Header                    deviceInfoHeader
	Flags                     uint32
	OutputTechnology          uint32
	EDIDManufactureID         uint16
	EDIDProductCodeID         uint16
	ConnectorInstance         uint32
	MonitorFriendlyDeviceName [64]uint16
	MonitorDevicePath         [128]uint16
}

type adapterName struct { // 276 bytes
	Header            deviceInfoHeader
	AdapterDevicePath [128]uint16
}

type advancedColorInfo struct { // 32 bytes
	Header              deviceInfoHeader
	Value               uint32 // bit 0 supported, bit 1 enabled
	ColorEncoding       uint32
	BitsPerColorChannel uint32
}

type setAdvancedColorState struct { // 24 bytes
	Header deviceInfoHeader
	Value  uint32 // bit 0 enable
}

type dpiScaleGet struct { // 32 bytes
	Header                 deviceInfoHeader
	MinRel, CurRel, MaxRel int32
}

type dpiScaleSet struct { // 24 bytes
	Header deviceInfoHeader
	Rel    int32
}

type displayConfig struct {
	paths []pathInfo
	modes []modeInfo
}

// queryActive reads the active paths. The buffers are heap slices, so the
// pointers stay valid for the whole call.
func queryActive() (displayConfig, error) {
	for range 4 {
		var np, nm uint32
		r, _, _ := syscall.SyscallN(procGetDisplayConfigBufferSizes.Addr(), qdcOnlyActivePaths,
			uintptr(unsafe.Pointer(&np)), uintptr(unsafe.Pointer(&nm)))
		if r != 0 {
			return displayConfig{}, fmt.Errorf("vdd: GetDisplayConfigBufferSizes: %w", syscall.Errno(r))
		}
		c := displayConfig{paths: make([]pathInfo, np+1), modes: make([]modeInfo, nm+1)}
		np, nm = uint32(len(c.paths)), uint32(len(c.modes))
		r, _, _ = syscall.SyscallN(procQueryDisplayConfig.Addr(), qdcOnlyActivePaths,
			uintptr(unsafe.Pointer(&np)), uintptr(unsafe.Pointer(&c.paths[0])),
			uintptr(unsafe.Pointer(&nm)), uintptr(unsafe.Pointer(&c.modes[0])), 0)
		if syscall.Errno(r) == windows.ERROR_INSUFFICIENT_BUFFER {
			continue
		}
		if r != 0 {
			return displayConfig{}, fmt.Errorf("vdd: QueryDisplayConfig: %w", syscall.Errno(r))
		}
		c.paths, c.modes = c.paths[:np], c.modes[:nm]
		return c, nil
	}
	return displayConfig{}, errors.New("vdd: QueryDisplayConfig: display layout kept changing")
}

func getDeviceInfo(h *deviceInfoHeader) error {
	if r, _, _ := syscall.SyscallN(procDisplayConfigGetDeviceInfo.Addr(), uintptr(unsafe.Pointer(h))); r != 0 {
		return fmt.Errorf("vdd: DisplayConfigGetDeviceInfo(%d): %w", h.Type, syscall.Errno(r))
	}
	return nil
}

func gdiName(p *pathInfo) (string, error) {
	n := new(sourceDeviceName)
	n.Header = deviceInfoHeader{Type: infoGetSourceName, Size: uint32(unsafe.Sizeof(*n)), AdapterID: p.Source.AdapterID, ID: p.Source.ID}
	if err := getDeviceInfo(&n.Header); err != nil {
		return "", err
	}
	return windows.UTF16ToString(n.GDIDeviceName[:]), nil
}

func adapterDevicePath(l windows.LUID) (string, error) {
	n := new(adapterName)
	n.Header = deviceInfoHeader{Type: infoGetAdapterName, Size: uint32(unsafe.Sizeof(*n)), AdapterID: l}
	if err := getDeviceInfo(&n.Header); err != nil {
		return "", err
	}
	return windows.UTF16ToString(n.AdapterDevicePath[:]), nil
}

// DisplayNames maps each active display's GDI name to its monitor's
// friendly name, as Windows's display settings show it ("LG ULTRAGEAR").
// A monitor that reports no name is left out.
func DisplayNames() (map[string]string, error) {
	c, err := queryActive()
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for i := range c.paths {
		p := &c.paths[i]
		gdi, err := gdiName(p)
		if err != nil {
			continue
		}
		n := new(targetDeviceName)
		n.Header = deviceInfoHeader{Type: infoGetTargetName, Size: uint32(unsafe.Sizeof(*n)), AdapterID: p.Target.AdapterID, ID: p.Target.ID}
		if getDeviceInfo(&n.Header) != nil {
			continue
		}
		if name := windows.UTF16ToString(n.MonitorFriendlyDeviceName[:]); name != "" {
			out[gdi] = name
		}
	}
	return out, nil
}

// findPath returns the active path to this IddCx target, if Windows shows it.
func (c displayConfig) findPath(idd gpu.LUID, target uint32) *pathInfo {
	for i := range c.paths {
		p := &c.paths[i]
		if luidOf(p.Target.AdapterID) == idd && p.Target.ID == target {
			return p
		}
	}
	return nil
}

// Monitors lists every SudoVDA monitor on the desktop, matched by the
// adapter's device instance against the driver's control interface.
func Monitors() ([]Monitor, error) {
	iface, err := devicePath()
	if err != nil {
		return nil, err
	}
	instance := deviceInstance(iface)
	c, err := queryActive()
	if err != nil {
		return nil, err
	}
	var out []Monitor
	seen := map[windows.LUID]bool{}
	for i := range c.paths {
		p := &c.paths[i]
		ours, ok := seen[p.Target.AdapterID]
		if !ok {
			path, err := adapterDevicePath(p.Target.AdapterID)
			ours = err == nil && deviceInstance(path) == instance
			seen[p.Target.AdapterID] = ours
		}
		if !ours {
			continue
		}
		m := Monitor{IddAdapter: luidOf(p.Target.AdapterID), TargetID: p.Target.ID}
		m.GDIName, _ = gdiName(p)
		out = append(out, m)
	}
	return out, nil
}

// deviceInstance takes `\\?\ROOT#DISPLAY#0001#{guid}` to `root#display#0001`.
func deviceInstance(interfacePath string) string {
	s := strings.ToLower(strings.TrimPrefix(interfacePath, `\\?\`))
	if i := strings.LastIndex(s, "#{"); i >= 0 {
		s = s[:i]
	}
	return s
}

const (
	devmodeSize          = 220
	dmFieldsOff          = 72
	dmPelsWidthOff       = 172
	dmPelsHeightOff      = 176
	dmFrequencyOff       = 184
	dmPelsWidth          = 0x80000
	dmPelsHeight         = 0x100000
	dmFrequency          = 0x400000
	enumCurrent          = ^uintptr(0)
	cdsUpdateRegistry    = 0x1
	dispChangeSuccessful = 0
)

type devmode [devmodeSize]byte

func (d *devmode) u32(off int) *uint32 { return (*uint32)(unsafe.Pointer(&d[off])) }

func currentMode(name string) (*devmode, error) {
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	dm := new(devmode)
	*(*uint16)(unsafe.Pointer(&dm[68])) = devmodeSize
	if r, _, _ := syscall.SyscallN(procEnumDisplaySettingsW.Addr(), uintptr(unsafe.Pointer(n)), enumCurrent, uintptr(unsafe.Pointer(dm))); r == 0 {
		return nil, fmt.Errorf("vdd: EnumDisplaySettings %s failed", name)
	}
	return dm, nil
}

// setMode changes the GDI mode when it differs. GDI reports and takes whole
// hertz (59.94 reads as 59), so a rate within 1 Hz counts as set; otherwise
// the driver's fractional preferred mode would be replaced.
func setMode(name string, w, h, milliHz int) error {
	dm, err := currentMode(name)
	if err != nil {
		return err
	}
	hz := uint32((milliHz + 500) / 1000)
	cur := int(*dm.u32(dmFrequencyOff)) * 1000
	if *dm.u32(dmPelsWidthOff) == uint32(w) && *dm.u32(dmPelsHeightOff) == uint32(h) && cur > milliHz-1000 && cur < milliHz+1000 {
		return nil
	}
	*dm.u32(dmFieldsOff) = dmPelsWidth | dmPelsHeight | dmFrequency
	*dm.u32(dmPelsWidthOff), *dm.u32(dmPelsHeightOff), *dm.u32(dmFrequencyOff) = uint32(w), uint32(h), hz
	n, _ := windows.UTF16PtrFromString(name)
	r, _, _ := syscall.SyscallN(procChangeDisplaySettingsExW.Addr(), uintptr(unsafe.Pointer(n)), uintptr(unsafe.Pointer(dm)), 0, cdsUpdateRegistry, 0)
	if int32(r) != dispChangeSuccessful {
		return fmt.Errorf("vdd: ChangeDisplaySettingsEx %s %dx%d@%d: code %d", name, w, h, hz, int32(r))
	}
	return nil
}

// makePrimary moves every source so this path's source sits at (0, 0).
func makePrimary(idd gpu.LUID, target uint32) error {
	c, err := queryActive()
	if err != nil {
		return err
	}
	p := c.findPath(idd, target)
	if p == nil || p.Source.ModeInfoIdx == modeIdxInvalid || int(p.Source.ModeInfoIdx) >= len(c.modes) {
		return errors.New("vdd: primary: display has no source mode")
	}
	own := &c.modes[p.Source.ModeInfoIdx]
	dx, dy := int32(own.Union[3]), int32(own.Union[4])
	if dx == 0 && dy == 0 {
		return nil
	}
	for i := range c.modes {
		m := &c.modes[i]
		if m.InfoType == modeInfoTypeSource {
			m.Union[3] = uint32(int32(m.Union[3]) - dx)
			m.Union[4] = uint32(int32(m.Union[4]) - dy)
		}
	}
	r, _, _ := syscall.SyscallN(procSetDisplayConfig.Addr(), uintptr(len(c.paths)), uintptr(unsafe.Pointer(&c.paths[0])),
		uintptr(len(c.modes)), uintptr(unsafe.Pointer(&c.modes[0])),
		sdcApply|sdcUseSuppliedDisplayConf|sdcSaveToDatabase|sdcAllowChanges)
	if r != 0 {
		return fmt.Errorf("vdd: SetDisplayConfig primary: %w", syscall.Errno(r))
	}
	return nil
}

// setHDR turns Windows advanced color on or off for the path's target and
// reports the state that results.
func setHDR(p *pathInfo, on bool) (bool, error) {
	info := new(advancedColorInfo)
	info.Header = deviceInfoHeader{Type: infoGetAdvancedColorInfo, Size: uint32(unsafe.Sizeof(*info)), AdapterID: p.Target.AdapterID, ID: p.Target.ID}
	if err := getDeviceInfo(&info.Header); err != nil {
		return false, err
	}
	supported, enabled := info.Value&1 != 0, info.Value&2 != 0
	if enabled == on {
		return enabled, nil
	}
	if on && !supported {
		return false, errors.New("vdd: virtual display does not support HDR")
	}
	set := new(setAdvancedColorState)
	set.Header = deviceInfoHeader{Type: infoSetAdvancedColorState, Size: uint32(unsafe.Sizeof(*set)), AdapterID: p.Target.AdapterID, ID: p.Target.ID}
	if on {
		set.Value = 1
	}
	if r, _, _ := syscall.SyscallN(procDisplayConfigSetDeviceInfo.Addr(), uintptr(unsafe.Pointer(&set.Header))); r != 0 {
		return enabled, fmt.Errorf("vdd: set HDR %v: %w", on, syscall.Errno(r))
	}
	return on, nil
}

// setScale sets the display scale of the path's source to the step
// nearest pct and reports the scale that results.
func setScale(p *pathInfo, pct int) (int, error) {
	get := new(dpiScaleGet)
	get.Header = deviceInfoHeader{Type: infoGetDPIScale, Size: uint32(unsafe.Sizeof(*get)), AdapterID: p.Source.AdapterID, ID: p.Source.ID}
	if err := getDeviceInfo(&get.Header); err != nil {
		return 0, err
	}
	minRel, maxRel := int(get.MinRel), int(get.MaxRel)
	rel := dpiRelative(pct, minRel, maxRel)
	if rel == int(get.CurRel) {
		return dpiPercent(minRel, rel), nil
	}
	set := new(dpiScaleSet)
	set.Header = deviceInfoHeader{Type: infoSetDPIScale, Size: uint32(unsafe.Sizeof(*set)), AdapterID: p.Source.AdapterID, ID: p.Source.ID}
	set.Rel = int32(rel)
	if r, _, _ := syscall.SyscallN(procDisplayConfigSetDeviceInfo.Addr(), uintptr(unsafe.Pointer(&set.Header))); r != 0 {
		return dpiPercent(minRel, int(get.CurRel)), fmt.Errorf("vdd: set display scale %d%%: %w", dpiPercent(minRel, rel), syscall.Errno(r))
	}
	return dpiPercent(minRel, rel), nil
}

// scale reads the display scale of the path's source, in percent.
func scale(p *pathInfo) (int, error) {
	get := new(dpiScaleGet)
	get.Header = deviceInfoHeader{Type: infoGetDPIScale, Size: uint32(unsafe.Sizeof(*get)), AdapterID: p.Source.AdapterID, ID: p.Source.ID}
	if err := getDeviceInfo(&get.Header); err != nil {
		return 0, err
	}
	return dpiPercent(int(get.MinRel), int(get.CurRel)), nil
}
