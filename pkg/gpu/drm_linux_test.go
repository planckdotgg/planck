package gpu

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRenderNodesReadSysfs(t *testing.T) {
	root := t.TempDir()
	dev, sys := filepath.Join(root, "dev"), filepath.Join(root, "sys")
	for _, n := range []struct {
		node, vendor, device, driver string
	}{
		{"renderD129", "0x1002\n", "0x73df\n", "amdgpu"},
		{"renderD128", "0x10de\n", "0x2704\n", "nvidia"},
	} {
		if err := os.MkdirAll(dev, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dev, n.node), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		d := filepath.Join(sys, n.node, "device")
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(d, "vendor"), []byte(n.vendor), 0o644)
		os.WriteFile(filepath.Join(d, "device"), []byte(n.device), 0o644)
		if err := os.Symlink(filepath.Join("/sys/bus/pci/drivers", n.driver), filepath.Join(d, "driver")); err != nil {
			t.Fatal(err)
		}
	}
	nodes, err := renderNodes(dev, sys)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 {
		t.Fatalf("%d nodes, want 2", len(nodes))
	}
	if n := nodes[0]; filepath.Base(n.Path) != "renderD128" || n.Adapter.Vendor != VendorNVIDIA || n.Adapter.DeviceID != 0x2704 || n.Driver != "nvidia" {
		t.Errorf("first node %+v", n)
	}
	if n := nodes[1]; n.Adapter.Vendor != VendorAMD || n.Adapter.Name != "AMD 0x73df (amdgpu)" {
		t.Errorf("second node %+v", n)
	}
}

func TestRenderNodesOnThisMachine(t *testing.T) {
	nodes, err := RenderNodes()
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		if n.Adapter.LUID == 0 {
			t.Errorf("%s: no device number", n.Path)
		}
		t.Logf("%s: %s, LUID %#x", n.Path, n.Adapter.Name, uint64(n.Adapter.LUID))
	}
}
