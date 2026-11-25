package gamepad

import (
	"math"
	"testing"
)

func TestDefaultCalibrationPassesThrough(t *testing.T) {
	s := State{Buttons: ButtonA, LT: 3, RT: 250, LX: 120, LY: -32768, RX: 32767, RY: -5}
	if got := DefaultCalibration().Apply(s); got != s {
		t.Errorf("Apply = %+v, want %+v", got, s)
	}
}

func TestRadialDeadzone(t *testing.T) {
	c := Calibration{LeftDeadzone: 20, RightDeadzone: 50}
	// 0.15 of full deflection on a diagonal: inside 20%.
	s := c.Apply(State{LX: 3500, LY: 3500, RX: 32767})
	if s.LX != 0 || s.LY != 0 {
		t.Errorf("inside deadzone = %d,%d, want 0,0", s.LX, s.LY)
	}
	if s.RX != 32767 {
		t.Errorf("full deflection = %d, want 32767", s.RX)
	}
	// Just outside the edge starts from zero, not from the edge.
	s = c.Apply(State{LX: 9830}) // 0.3
	want := toInt16((0.3 - 0.2) / 0.8)
	if d := int(s.LX) - int(want); d < -2 || d > 2 {
		t.Errorf("rescaled = %d, want ~%d", s.LX, want)
	}
	// Direction is kept.
	s = c.Apply(State{LX: -20000, LY: 20000})
	if s.LX >= 0 || s.LY <= 0 || math.Abs(float64(s.LX)+float64(s.LY)) > 2 {
		t.Errorf("diagonal = %d,%d", s.LX, s.LY)
	}
	over, at50 := (Calibration{LeftDeadzone: 90}).Apply(State{LX: 19660}), (Calibration{LeftDeadzone: 50}).Apply(State{LX: 19660})
	if over.LX == 0 || over != at50 {
		t.Errorf("deadzone 90%% = %d, want the 50%% result %d", over.LX, at50.LX)
	}
}

func TestInvertYAndTriggerThreshold(t *testing.T) {
	c := Calibration{InvertLeftY: true, TriggerThreshold: 10}
	s := c.Apply(State{LY: 1000, RY: 1000, LT: 20, RT: 255})
	if s.LY != -1000 || s.RY != 1000 {
		t.Errorf("invert = %d,%d", s.LY, s.RY)
	}
	if s.LT != 0 || s.RT != 255 {
		t.Errorf("thresholds = %d,%d", s.LT, s.RT)
	}
	if s := c.Apply(State{LY: -32768}); s.LY != 32767 {
		t.Errorf("invert of -32768 = %d", s.LY)
	}
	if s := c.Apply(State{LT: 140}); s.LT < 125 || s.LT > 130 {
		t.Errorf("rescaled trigger = %d", s.LT)
	}
}

func TestScaleRumble(t *testing.T) {
	if l, s := DefaultCalibration().ScaleRumble(65535, 32768); l != 255 || s != 128 {
		t.Errorf("full = %d,%d", l, s)
	}
	if l, s := (Calibration{RumbleStrength: 50}).ScaleRumble(65535, 0); l != 128 || s != 0 {
		t.Errorf("half = %d,%d", l, s)
	}
	if l, s := (Calibration{RumbleOff: true, RumbleStrength: 100}).ScaleRumble(65535, 65535); l != 0 || s != 0 {
		t.Errorf("off = %d,%d", l, s)
	}
	if l, _ := (Calibration{RumbleStrength: -10}).ScaleRumble(65535, 0); l != 0 {
		t.Errorf("negative strength = %d", l)
	}
}
