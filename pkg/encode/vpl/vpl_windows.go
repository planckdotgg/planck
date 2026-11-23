//go:build windows

// Package vpl probes Intel Quick Sync through the oneVPL dispatcher
// (libvpl.dll), with no cgo and no Intel SDK at build time.
package vpl

import (
	"encoding/binary"
	"fmt"
	"syscall"
	"unsafe"

	"github.com/planckdotgg/planck/internal/win"
)

var (
	dll               = syscall.NewLazyDLL("libvpl.dll")
	procLoad          = dll.NewProc("MFXLoad")
	procUnload        = dll.NewProc("MFXUnload")
	procEnum          = dll.NewProc("MFXEnumImplementations")
	procRelease       = dll.NewProc("MFXDispReleaseImplDescription")
	procCreateSession = dll.NewProc("MFXCreateSession")
	procClose         = dll.NewProc("MFXClose")
	procQueryVersion  = dll.NewProc("MFXQueryVersion")
	procQueryIMPL     = dll.NewProc("MFXQueryIMPL")
)

// mfxImplDescription offsets (pack 8, 64-bit).
const (
	idImpl         = 4
	idAccel        = 8
	idAPIVersion   = 12
	idImplName     = 16
	idVendorID     = 304
	idDevDeviceID  = 312 + 16
	idEncNumCodecs = 504 + 16
	idEncCodecs    = 504 + 24

	encSize        = 32
	encCodecID     = 0
	encMaxLevel    = 4
	encNumProfiles = 22
	encProfiles    = 24

	profSize    = 32
	profProfile = 0
	profNumMem  = 18
	profMemDesc = 24

	memSize       = 56
	memHandleType = 0
	memWidthMax   = 8
	memHeightMax  = 20
	memNumFormats = 42
	memFormats    = 48
)

type Profile struct {
	Profile   uint32
	MaxWidth  uint32
	MaxHeight uint32
	Formats   []string
}

type Codec struct {
	FourCC   string
	MaxLevel uint16
	Profiles []Profile
}

type Impl struct {
	Name       string
	Impl       uint32
	Accel      uint32
	APIVersion string
	VendorID   uint32
	DeviceID   string
	Encoders   []Codec
	Session    string
}

func fourcc(v uint32) string {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, v)
	return string(b)
}

func cstr(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

func mem(p uintptr, n int) []byte { return unsafe.Slice((*byte)(win.Ptr(p)), n) }

func u16(b []byte, o int) uint16  { return binary.LittleEndian.Uint16(b[o:]) }
func u32(b []byte, o int) uint32  { return binary.LittleEndian.Uint32(b[o:]) }
func ptr(b []byte, o int) uintptr { return uintptr(binary.LittleEndian.Uint64(b[o:])) }

// Probe lists every implementation the dispatcher finds, with its encoders,
// and opens a session on each to prove the runtime loads.
func Probe() ([]Impl, error) {
	if err := procLoad.Find(); err != nil {
		return nil, err
	}
	loader, _, _ := procLoad.Call()
	if loader == 0 {
		return nil, fmt.Errorf("vpl: MFXLoad returned no loader")
	}
	defer procUnload.Call(loader)
	var out []Impl
	for i := 0; ; i++ {
		var desc uintptr
		r, _, _ := procEnum.Call(loader, uintptr(i), 1, uintptr(unsafe.Pointer(&desc)))
		if r != 0 {
			break
		}
		d := mem(desc, 640)
		impl := Impl{
			Name:       cstr(d[idImplName : idImplName+32]),
			Impl:       u32(d, idImpl),
			Accel:      u32(d, idAccel),
			APIVersion: fmt.Sprintf("%d.%d", u16(d, idAPIVersion+2), u16(d, idAPIVersion)),
			VendorID:   u32(d, idVendorID),
			DeviceID:   cstr(d[idDevDeviceID : idDevDeviceID+128]),
		}
		n := int(u16(d, idEncNumCodecs))
		codecs := ptr(d, idEncCodecs)
		for c := 0; c < n; c++ {
			e := mem(codecs+uintptr(c*encSize), encSize)
			codec := Codec{FourCC: fourcc(u32(e, encCodecID)), MaxLevel: u16(e, encMaxLevel)}
			profs := ptr(e, encProfiles)
			for p := 0; p < int(u16(e, encNumProfiles)); p++ {
				pr := mem(profs+uintptr(p*profSize), profSize)
				prof := Profile{Profile: u32(pr, profProfile)}
				mds := ptr(pr, profMemDesc)
				for m := 0; m < int(u16(pr, profNumMem)); m++ {
					md := mem(mds+uintptr(m*memSize), memSize)
					prof.MaxWidth = max(prof.MaxWidth, u32(md, memWidthMax))
					prof.MaxHeight = max(prof.MaxHeight, u32(md, memHeightMax))
					fmts := ptr(md, memFormats)
					for f := 0; f < int(u16(md, memNumFormats)) && m == 0; f++ {
						prof.Formats = append(prof.Formats, fourcc(u32(mem(fmts+uintptr(f*4), 4), 0)))
					}
				}
				codec.Profiles = append(codec.Profiles, prof)
			}
			impl.Encoders = append(impl.Encoders, codec)
		}
		procRelease.Call(loader, desc)

		var session uintptr
		r, _, _ = procCreateSession.Call(loader, uintptr(i), uintptr(unsafe.Pointer(&session)))
		if r != 0 {
			impl.Session = fmt.Sprintf("MFXCreateSession status %d", int32(r))
		} else {
			var ver uint32
			var implType int32
			procQueryVersion.Call(session, uintptr(unsafe.Pointer(&ver)))
			procQueryIMPL.Call(session, uintptr(unsafe.Pointer(&implType)))
			impl.Session = fmt.Sprintf("session ok, API %d.%d, impl 0x%x", ver>>16, ver&0xffff, implType)
			procClose.Call(session)
		}
		out = append(out, impl)
	}
	return out, nil
}
