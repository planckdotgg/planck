//go:build windows

package main

import (
	"flag"
	"fmt"

	"github.com/planckdotgg/planck/pkg/encode"
	"github.com/planckdotgg/planck/pkg/encode/amf"
	"github.com/planckdotgg/planck/pkg/encode/nvenc"
	"github.com/planckdotgg/planck/pkg/encode/vpl"
)

func runVendors(args []string) error {
	if d, err := openDevice("NVIDIA"); err != nil {
		fmt.Println("NVENC: no device:", err)
	} else {
		maj, min, _ := nvenc.MaxAPIVersion()
		caps, err := nvenc.Probe(d.dev, d.info)
		fmt.Printf("NVENC on %s: API %d.%d, codecs=%v chroma=%v engines=%d subframe=%v err=%v\n",
			d.info.Name, maj, min, caps.Codecs, caps.Chroma, caps.Engines, caps.SubFrame, err)
		d.Close()
	}

	ver, err := amf.Version()
	fmt.Printf("\nAMF runtime %s (err=%v)\n", ver, err)
	for _, name := range []string{"AMD", "NVIDIA", "Intel"} {
		d, err := openDevice(name)
		if err != nil {
			continue
		}
		caps, err := amf.Probe(d.dev)
		if err != nil {
			fmt.Printf("  AMF on %s: %v\n", d.info.Name, err)
			d.Close()
			continue
		}
		for _, c := range caps {
			if c.Err != nil {
				fmt.Printf("  AMF on %s %s: %v\n", d.info.Name, c.Codec, c.Err)
				continue
			}
			fmt.Printf("  AMF on %s %s: accel=%d maxBitrate=%d maxProfile=%d streams=%d hwInstances=%d maxThroughputMB=%d sliceOutput=%v width=%v height=%v inputFormats=%v\n",
				d.info.Name, c.Codec, c.Acceleration, c.MaxBitrate, c.MaxProfile, c.Streams, c.HWInstances, c.MaxThroughputMB, c.SliceOutput, c.Width, c.Height, c.InputFormats)
		}
		d.Close()
	}

	impls, err := vpl.Probe()
	fmt.Printf("\noneVPL dispatcher (libvpl.dll): %d implementations (err=%v)\n", len(impls), err)
	for _, im := range impls {
		fmt.Printf("  %q impl=%d accel=0x%x api=%s vendor=0x%x device=%q %s\n", im.Name, im.Impl, im.Accel, im.APIVersion, im.VendorID, im.DeviceID, im.Session)
		for _, c := range im.Encoders {
			fmt.Printf("    encoder %q maxLevel=%d profiles:", c.FourCC, c.MaxLevel)
			for _, p := range c.Profiles {
				fmt.Printf(" %d(%dx%d %v)", p.Profile, p.MaxWidth, p.MaxHeight, p.Formats)
			}
			fmt.Println()
		}
	}
	return nil
}

func runAMF(args []string) error {
	fs := flag.NewFlagSet("amf", flag.ExitOnError)
	frames := fs.Int("frames", 600, "measured frames")
	pace := fs.Bool("pace", true, "submit at the frame rate")
	fs.Parse(args)
	d, err := openDevice("AMD")
	if err != nil {
		return err
	}
	defer d.Close()
	open := func(cfg encode.Config, c encodeCase) (encode.Encoder, error) { return amf.Open(d.dev, d.info, cfg) }
	fmt.Printf("# AMF HEVC on %s (luid 0x%x)\n\n", d.info.Name, uint64(d.info.LUID))
	printEncodeHeader()
	for _, s := range []struct{ w, h, kbps int }{{1920, 1080, 50000}, {2560, 1440, 80000}} {
		for _, fps := range []int{120, 240} {
			kbps := s.kbps * fps / 120
			for _, c := range []encodeCase{
				{"hevc", "nv12", "bgra", s.w, s.h, fps, kbps, 0, false, 0, 0},
				{"hevc", "nv12", "nv12", s.w, s.h, fps, kbps, 0, false, 0, 0},
				{"hevc", "p010", "rgb10", s.w, s.h, fps, kbps, 0, false, 0, 0},
				{"hevc", "p010", "p010", s.w, s.h, fps, kbps, 0, false, 0, 0},
				{"hevc", "nv12", "bgra", s.w, s.h, fps, kbps, 4, false, 0, 0},
			} {
				printEncodeRow(c, runEncodeCase(d, c, *frames, *pace, open))
			}
		}
	}
	return nil
}
