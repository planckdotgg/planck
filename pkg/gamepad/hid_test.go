package gamepad

import "testing"

func TestHIDLayout(t *testing.T) {
	l := newHIDLayout([]hidElement{
		{page: pageButton, usage: 2, cookie: 11},
		{page: pageGenericDesktop, usage: usageRz, cookie: 5},
		{page: pageButton, usage: 1, cookie: 10},
		{page: pageGenericDesktop, usage: usageX, cookie: 3},
		{page: pageGenericDesktop, usage: usageHat, cookie: 7},
		{page: pageButton, usage: 1, cookie: 10}, // the same element twice
		{page: 0xFF00, usage: 0x20, cookie: 12},  // vendor page: ignored
		{page: pageGenericDesktop, usage: usageY, cookie: 4},
	})
	if len(l.buttons) != 2 || l.buttons[0].usage != 1 || l.buttons[1].usage != 2 {
		t.Errorf("buttons = %+v", l.buttons)
	}
	if len(l.axes) != 3 || l.axes[0].usage != usageX || l.axes[2].usage != usageRz {
		t.Errorf("axes = %+v", l.axes)
	}
	if len(l.hats) != 1 {
		t.Errorf("hats = %+v", l.hats)
	}
}

func TestHatBits(t *testing.T) {
	for _, tc := range []struct {
		v, min, max int32
		want        uint8
	}{
		{0, 0, 7, HatUp}, {1, 0, 7, HatUp | HatRight}, {4, 0, 7, HatDown}, {7, 0, 7, HatLeft | HatUp},
		{8, 0, 7, 0}, {15, 0, 7, 0}, // centred: a null state outside the range
		{1, 1, 8, HatUp}, {3, 0, 3, HatLeft}, {2, 0, 3, HatDown}, {0, 0, 1, 0},
	} {
		if got := hatBits(tc.v, tc.min, tc.max); got != tc.want {
			t.Errorf("hatBits(%d, %d..%d) = %d, want %d", tc.v, tc.min, tc.max, got, tc.want)
		}
	}
}

func axes(usages ...[2]uint32) []hidElement {
	var out []hidElement
	for i, u := range usages {
		out = append(out, hidElement{page: u[0], usage: u[1], cookie: uint32(i)})
	}
	return out
}

func TestHIDDefaultBindings(t *testing.T) {
	gd := func(u uint32) [2]uint32 { return [2]uint32{pageGenericDesktop, u} }
	sim := func(u uint32) [2]uint32 { return [2]uint32{pageSimulation, u} }
	ax := func(i int) Binding { return Binding{Kind: SourceAxis, Index: i} }
	for _, tc := range []struct {
		name           string
		axes           []hidElement
		rx, ry, lt, rt Binding
	}{
		// DualShock 4: X Y Z Rx Ry Rz, triggers on Rx and Ry.
		{"ds4", axes(gd(usageX), gd(usageY), gd(usageZ), gd(usageRx), gd(usageRy), gd(usageRz)), ax(2), ax(5), ax(3), ax(4)},
		// Xbox over Bluetooth: X Y Z Rz, Brake and Accelerator.
		{"xbox bt", axes(gd(usageX), gd(usageY), gd(usageZ), gd(usageRz), sim(usageAccelerator), sim(usageBrake)), ax(2), ax(3), ax(5), ax(4)},
	} {
		b := hidDefaultBindings(tc.axes)
		if b[InputLeftX] != ax(0) || b[InputLeftY] != ax(1) {
			t.Errorf("%s: left stick = %v %v", tc.name, b[InputLeftX], b[InputLeftY])
		}
		if b[InputRightX] != tc.rx || b[InputRightY] != tc.ry || b[InputLeftTrigger] != tc.lt || b[InputRightTrigger] != tc.rt {
			t.Errorf("%s: right %v %v triggers %v %v", tc.name, b[InputRightX], b[InputRightY], b[InputLeftTrigger], b[InputRightTrigger])
		}
		if b[InputA] != defaultBindings[InputA] {
			t.Errorf("%s: buttons left the XInput order", tc.name)
		}
	}
	// No Rz: the right stick is Rx and Ry, triggers Z and Rz.
	b := hidDefaultBindings(axes(gd(usageX), gd(usageY), gd(usageRx), gd(usageRy), gd(usageZ)))
	if b[InputRightX] != ax(2) || b[InputRightY] != ax(3) || b[InputLeftTrigger] != ax(4) || b[InputRightTrigger].Kind != SourceNone {
		t.Errorf("rx/ry stick: %v %v %v %v", b[InputRightX], b[InputRightY], b[InputLeftTrigger], b[InputRightTrigger])
	}
}

func TestRumbleReport(t *testing.T) {
	ds4 := ControllerInfo{VendorID: 0x054C, ProductID: 0x09CC, Connection: ConnectionUSB}
	r := rumbleReport(ds4, 200, 50)
	if len(r) != 32 || r[0] != 0x05 || r[4] != 50 || r[5] != 200 {
		t.Errorf("DS4 report = %v", r)
	}
	ds := ControllerInfo{VendorID: 0x054C, ProductID: 0x0CE6, Connection: ConnectionUSB}
	if r := rumbleReport(ds, 200, 50); len(r) != 63 || r[0] != 0x02 || r[3] != 50 || r[4] != 200 {
		t.Errorf("DualSense report = %v", r)
	}
	ds4.Connection = ConnectionBluetooth
	if rumbleReport(ds4, 1, 1) != nil || rumbleReport(ControllerInfo{VendorID: 0x045E, Connection: ConnectionUSB}, 1, 1) != nil {
		t.Error("report for a pad with no known USB rumble report")
	}
}
