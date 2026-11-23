//go:build windows

package main

import (
	"fmt"
	"syscall"

	"github.com/planckdotgg/planck/internal/win"
)

func runEnum(args []string) error {
	f, err := win.CreateFactory()
	if err != nil {
		return fmt.Errorf("CreateDXGIFactory1: %w", err)
	}
	defer f.Release()
	adapters, err := f.Adapters()
	if err != nil {
		return err
	}
	for i, a := range adapters {
		info, err := a.Info()
		if err != nil {
			return err
		}
		fmt.Printf("adapter %d: %s  vendor=%s dev=0x%04x luid=0x%016x vram=%dMiB software=%v\n",
			i, info.Name, info.Vendor, info.DeviceID, uint64(info.LUID), info.VRAMBytes>>20, info.Software)
		outs, err := a.Outputs()
		if err != nil {
			fmt.Printf("  outputs: %v\n", err)
		}
		for j, o := range outs {
			d, err := o.Desc()
			if err != nil {
				fmt.Printf("  output %d: %v\n", j, err)
				continue
			}
			r := d.DesktopCoordinates
			line := fmt.Sprintf("  output %d: %s %dx%d at (%d,%d) attached=%v", j, d.Name(),
				r.Right-r.Left, r.Bottom-r.Top, r.Left, r.Top, d.AttachedToDesktop != 0)
			if d1, err := o.Desc1(); err == nil {
				line += fmt.Sprintf(" bpc=%d colorspace=%d maxnits=%.0f", d1.BitsPerColor, d1.ColorSpace, d1.MaxLuminance)
			}
			fmt.Println(line)
			o.Release()
		}
		a.Release()
	}
	fmt.Println()
	for _, dll := range []struct{ name, symbol string }{
		{"nvEncodeAPI64.dll", "NvEncodeAPICreateInstance"},
		{"nvcuda.dll", "cuInit"},
		{"amfrt64.dll", "AMFInit"},
		{"libvpl.dll", "MFXLoad"},
		{"libmfxhw64.dll", "MFXInitEx"},
		{"libmfx64-gen.dll", "MFXInitialize"},
		{"vpl.dll", "MFXLoad"},
	} {
		l := syscall.NewLazyDLL(dll.name)
		if err := l.Load(); err != nil {
			fmt.Printf("dll %-18s not loadable: %v\n", dll.name, err)
			continue
		}
		p := l.NewProc(dll.symbol)
		if err := p.Find(); err != nil {
			fmt.Printf("dll %-18s loaded at 0x%x, %s missing: %v\n", dll.name, l.Handle(), dll.symbol, err)
			continue
		}
		fmt.Printf("dll %-18s loaded at 0x%x, %s at 0x%x\n", dll.name, l.Handle(), dll.symbol, p.Addr())
	}
	return nil
}
