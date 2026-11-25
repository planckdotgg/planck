package gamepad

import "testing"

func TestParseInput(t *testing.T) {
	for in := range NumInputs {
		got, err := ParseInput(in.String())
		if err != nil || got != in {
			t.Errorf("ParseInput(%q) = %v, %v", in.String(), got, err)
		}
	}
	if _, err := ParseInput("paddle9"); err == nil {
		t.Error("ParseInput(paddle9) took an unknown name")
	}
}

func TestLearn(t *testing.T) {
	stickAxis := func(v int32) Axis { return Axis{Value: v, Min: -32768, Max: 32767} }
	trig := func(v int32) Axis { return Axis{Value: v, Min: 0, Max: 255} }
	rest := func() RawState {
		return RawState{Buttons: make([]bool, 4), Hats: []uint8{0},
			Axes: []Axis{stickAxis(0), stickAxis(0), trig(0)}}
	}
	cases := []struct {
		name   string
		target Input
		move   func(r *RawState)
		want   Binding
		ok     bool
	}{
		{"button", InputA, func(r *RawState) { r.Buttons[2] = true }, Binding{Kind: SourceButton, Index: 2}, true},
		{"hat", InputDPadLeft, func(r *RawState) { r.Hats[0] = HatLeft }, Binding{Kind: SourceHat, HatMask: HatLeft}, true},
		{"axis half for a button", InputB, func(r *RawState) { r.Axes[1] = stickAxis(-30000) },
			Binding{Kind: SourceAxis, Index: 1, Range: RangeNegative}, true},
		{"stick pushed right", InputLeftX, func(r *RawState) { r.Axes[0] = stickAxis(30000) }, Binding{Kind: SourceAxis}, true},
		{"stick pushed the other way", InputLeftY, func(r *RawState) { r.Axes[1] = stickAxis(-30000) },
			Binding{Kind: SourceAxis, Index: 1, Invert: true}, true},
		{"stick ignores buttons", InputRightX, func(r *RawState) { r.Buttons[0] = true }, Binding{}, false},
		{"trigger axis", InputRightTrigger, func(r *RawState) { r.Axes[2] = trig(255) }, Binding{Kind: SourceAxis, Index: 2}, true},
		{"trigger button", InputLeftTrigger, func(r *RawState) { r.Buttons[3] = true }, Binding{Kind: SourceButton, Index: 3}, true},
		{"drift is not a move", InputA, func(r *RawState) { r.Axes[0] = stickAxis(6000) }, Binding{}, false},
	}
	for _, c := range cases {
		r, n := rest(), rest()
		c.move(&n)
		got, ok := Learn(&r, &n, c.target)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: Learn = %+v, %v; want %+v, %v", c.name, got, ok, c.want, c.ok)
		}
	}

	held := rest()
	held.Buttons[1] = true
	if _, ok := Learn(&held, &held, InputA); ok {
		t.Error("a button held since rest was learned")
	}
}
