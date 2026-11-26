package gpu

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// RenderNode is a DRM render node, /dev/dri/renderD*, which decoders,
// encoders and presenting surfaces open without a display's master rights.
type RenderNode struct {
	Path    string
	Driver  string
	Adapter Adapter
}

// RenderNodes lists the system's render nodes in number order. On Linux an
// Adapter's LUID is its render node's device number (st_rdev), the same
// for every process during a boot; its vendor and device come from sysfs.
func RenderNodes() ([]RenderNode, error) { return renderNodes("/dev/dri", "/sys/class/drm") }

func renderNodes(devDir, sysDir string) ([]RenderNode, error) {
	paths, err := filepath.Glob(filepath.Join(devDir, "renderD*"))
	if err != nil {
		return nil, err
	}
	sort.Slice(paths, func(i, j int) bool { return nodeNumber(paths[i]) < nodeNumber(paths[j]) })
	var out []RenderNode
	for _, p := range paths {
		var st unix.Stat_t
		if err := unix.Stat(p, &st); err != nil {
			continue
		}
		name := filepath.Base(p)
		dev := filepath.Join(sysDir, name, "device")
		vendor := readHex(filepath.Join(dev, "vendor"))
		device := readHex(filepath.Join(dev, "device"))
		driver := ""
		if link, err := os.Readlink(filepath.Join(dev, "driver")); err == nil {
			driver = filepath.Base(link)
		}
		a := Adapter{LUID: LUID(st.Rdev), Vendor: Vendor(vendor), DeviceID: uint32(device)}
		a.Name = adapterName(a, driver)
		a.Software = driver == "vgem" || driver == "vkms"
		out = append(out, RenderNode{Path: p, Driver: driver, Adapter: a})
	}
	return out, nil
}

// RenderNodeFor is the first render node of vendor, or of any vendor for
// VendorUnknown.
func RenderNodeFor(vendor Vendor) (RenderNode, error) {
	nodes, err := RenderNodes()
	if err != nil {
		return RenderNode{}, err
	}
	for _, n := range nodes {
		if vendor == VendorUnknown || n.Adapter.Vendor == vendor {
			return n, nil
		}
	}
	return RenderNode{}, fmt.Errorf("gpu: no render node for %v", vendor)
}

func nodeNumber(p string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(filepath.Base(p), "renderD"))
	return n
}

func readHex(path string) uint64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	v, _ := strconv.ParseUint(strings.TrimPrefix(strings.TrimSpace(string(b)), "0x"), 16, 32)
	return v
}

func adapterName(a Adapter, driver string) string {
	name := a.Vendor.String()
	if a.Vendor == VendorUnknown {
		name = "GPU"
	}
	if a.DeviceID != 0 {
		name += fmt.Sprintf(" 0x%04x", a.DeviceID)
	}
	if driver != "" {
		name += " (" + driver + ")"
	}
	return name
}
