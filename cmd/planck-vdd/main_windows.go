//go:build windows

// Command planck-vdd checks the SudoVDA virtual display driver by hand.
//
//	planck-vdd info
//	planck-vdd create [-w 1920] [-h 1080] [-hz 120] [-gpu 4080] [-hdr] [-primary] [-seed planck-vdd] [-hold 3s]
//	planck-vdd remove [-seed planck-vdd | -id {guid}]
//
// create adds a display, captures it with Desktop Duplication, holds it, removes
// it, and then checks that Windows shows no SudoVDA monitor with its target.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sys/windows"

	"github.com/planckdotgg/planck/pkg/capture"
	"github.com/planckdotgg/planck/pkg/capture/dxgi"
	"github.com/planckdotgg/planck/pkg/vdd"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: planck-vdd info|create|remove [flags]")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "info":
		err = info()
	case "create":
		err = create(os.Args[2:])
	case "remove":
		err = removeCmd(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "planck-vdd:", err)
		os.Exit(1)
	}
}

func info() error {
	i, err := vdd.DriverInfo()
	fmt.Printf("device:       %s\n", i.DevicePath)
	fmt.Printf("registry gpu: %q\n", i.RegistryGPUName)
	fmt.Printf("max monitors: %d\n", i.MaxMonitors)
	if err != nil {
		return err
	}
	fmt.Printf("protocol:     %v\n", i.Protocol)
	fmt.Printf("watchdog:     %v\n", i.Watchdog)
	adapters, err := dxgi.Adapters()
	if err != nil {
		return err
	}
	for _, a := range adapters {
		fmt.Printf("adapter:      0x%016x %s\n", uint64(a.LUID), a.Name)
	}
	return printMonitors()
}

func printMonitors() error {
	ms, err := vdd.Monitors()
	if err != nil {
		return err
	}
	fmt.Printf("sudovda monitors on the desktop: %d\n", len(ms))
	for _, m := range ms {
		fmt.Printf("  %s target %d idd 0x%x\n", m.GDIName, m.TargetID, uint64(m.IddAdapter))
	}
	return nil
}

func create(args []string) (err error) {
	fs := flag.NewFlagSet("create", flag.ExitOnError)
	w := fs.Int("w", 1920, "width")
	h := fs.Int("h", 1080, "height")
	hz := fs.Float64("hz", 120, "refresh rate in Hz")
	gpuName := fs.String("gpu", "", "render on the adapter whose name contains this (empty: driver's choice)")
	hdr := fs.Bool("hdr", false, "turn HDR on")
	primary := fs.Bool("primary", false, "make the display primary")
	seed := fs.String("seed", "planck-vdd", "client identity the monitor GUID derives from")
	hold := fs.Duration("hold", 3*time.Second, "keep the display this long (Ctrl+C ends early)")
	fs.Parse(args)

	spec := vdd.DisplaySpec{Width: *w, Height: *h, RefreshMilliHz: int(*hz*1000 + 0.5),
		ID: vdd.MonitorID([]byte(*seed)), HDR: *hdr, Primary: *primary}
	var adapterName string
	if *gpuName != "" {
		a, err := dxgi.FindAdapter(*gpuName)
		if err != nil {
			return err
		}
		spec.RenderAdapter, adapterName = a.LUID, a.Name
	}

	drv, err := vdd.Open()
	if err != nil {
		return err
	}
	defer func() {
		if r := recover(); r != nil {
			drv.Close()
			panic(r)
		}
	}()
	defer drv.Close()

	start := time.Now()
	v, err := drv.CreateDisplay(spec)
	if errors.Is(err, vdd.ErrAccessDenied) && adapterName != "" {
		fmt.Fprintf(os.Stderr, "run once from an elevated PowerShell to render SudoVDA on %s:\n  %s\n",
			adapterName, vdd.ElevatedGPUNameCommand(adapterName))
	}
	if err != nil {
		return err
	}
	fmt.Printf("created %s in %v: %s target %d idd 0x%x render 0x%x %dx%d@%.3f hdr=%v\n",
		v.ID, time.Since(start).Round(time.Millisecond), v.GDIName, v.TargetID,
		uint64(v.IddAdapter), uint64(v.RenderAdapter), v.Width, v.Height, float64(v.RefreshMilliHz)/1000, v.HDR)
	if spec.RenderAdapter != 0 {
		fmt.Printf("render adapter matches %s: %v\n", adapterName, v.RenderAdapter == spec.RenderAdapter)
	}
	if err := captureFrames(v.GDIName); err != nil {
		fmt.Println("capture:", err)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case <-time.After(*hold):
	case <-stop:
	case err := <-drv.Err():
		fmt.Println("driver:", err)
	}

	if err := drv.Close(); err != nil {
		return err
	}
	return verifyGone(v)
}

func captureFrames(gdiName string) error {
	src, err := dxgi.OpenDisplay(gdiName, dxgi.Options{})
	if err != nil {
		return err
	}
	defer src.Close()
	frames, recovers := 0, 0
	for range 30 {
		_, err := src.Next(50 * time.Millisecond)
		if errors.Is(err, capture.ErrTimeout) {
			continue
		}
		if errors.Is(err, capture.ErrAccessLost) && recovers < 3 {
			recovers++
			if err = src.Recover(2*time.Second, 50*time.Millisecond); err == nil {
				continue
			}
		}
		if err != nil {
			return err
		}
		frames++
		src.Release()
	}
	w, h := src.Size()
	fmt.Printf("capture: %d frames from %s on %s, %dx%d %v (%d access-lost recoveries)\n",
		frames, gdiName, src.Adapter().Name, w, h, src.Format(), recovers)
	return nil
}

func verifyGone(v *vdd.VirtualDisplay) error {
	deadline := time.Now().Add(4 * time.Second)
	for {
		ms, err := vdd.Monitors()
		if err != nil {
			return err
		}
		left := false
		for _, m := range ms {
			left = left || m.IddAdapter == v.IddAdapter && m.TargetID == v.TargetID
		}
		if !left {
			fmt.Printf("removed: target %d gone; %d sudovda monitors left on the desktop\n", v.TargetID, len(ms))
			return printMonitors()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("target %d still on the desktop 4 s after close", v.TargetID)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func removeCmd(args []string) error {
	fs := flag.NewFlagSet("remove", flag.ExitOnError)
	seed := fs.String("seed", "planck-vdd", "client identity the monitor GUID derives from")
	idText := fs.String("id", "", "monitor GUID, overrides -seed")
	fs.Parse(args)
	id := vdd.MonitorID([]byte(*seed))
	if *idText != "" {
		g, err := windows.GUIDFromString(*idText)
		if err != nil {
			return err
		}
		id = vdd.GUID(g)
	}
	drv, err := vdd.Open()
	if err != nil {
		return err
	}
	defer drv.Close()
	if err := drv.Remove(id); err != nil {
		return err
	}
	fmt.Println("removed", id)
	return nil
}
