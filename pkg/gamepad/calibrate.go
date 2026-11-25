package gamepad

import "math"

// Calibration is the user's processing of a mapped State, in percent.
type Calibration struct {
	LeftDeadzone     int // 0..50 of the stick's radius
	RightDeadzone    int
	TriggerThreshold int // 0..50 of the trigger's travel
	InvertLeftY      bool
	InvertRightY     bool
	RumbleOff        bool
	RumbleStrength   int // 0..100
}

const (
	MaxDeadzone         = 50
	MaxTriggerThreshold = 50
	MaxRumbleStrength   = 100
)

// DefaultCalibration passes input through unchanged at full rumble: games
// apply their own deadzones, and a second one would widen theirs.
func DefaultCalibration() Calibration { return Calibration{RumbleStrength: MaxRumbleStrength} }

func percent(p, limit int) float64 { return float64(max(0, min(limit, p))) / 100 }

// Apply runs s through the deadzones, trigger thresholds and Y inversion.
// A deadzone is radial and rescales what is outside it, so the stick still
// reaches full deflection.
func (c Calibration) Apply(s State) State {
	s.LX, s.LY = radial(s.LX, s.LY, percent(c.LeftDeadzone, MaxDeadzone), c.InvertLeftY)
	s.RX, s.RY = radial(s.RX, s.RY, percent(c.RightDeadzone, MaxDeadzone), c.InvertRightY)
	th := percent(c.TriggerThreshold, MaxTriggerThreshold)
	s.LT, s.RT = threshold(s.LT, th), threshold(s.RT, th)
	return s
}

func axisNorm(v int16) float64 {
	if v < 0 {
		return float64(v) / 32768
	}
	return float64(v) / 32767
}

func radial(x, y int16, dz float64, invertY bool) (int16, int16) {
	if invertY {
		y = -max(y, -32767)
	}
	if dz <= 0 {
		return x, y
	}
	fx, fy := axisNorm(x), axisNorm(y)
	mag := math.Hypot(fx, fy)
	if mag <= dz {
		return 0, 0
	}
	scale := math.Min(1, (mag-dz)/(1-dz)) / mag
	return toInt16(fx * scale), toInt16(fy * scale)
}

func threshold(v uint8, th float64) uint8 {
	if th <= 0 {
		return v
	}
	f := float64(v) / 255
	if f <= th {
		return 0
	}
	return uint8(math.Round(math.Min(1, (f-th)/(1-th)) * 255))
}

// ScaleRumble turns a host's rumble, 0..65535 per motor, into the motor
// strengths to drive here.
func (c Calibration) ScaleRumble(low, high uint16) (large, small uint8) {
	if c.RumbleOff {
		return 0, 0
	}
	k := percent(c.RumbleStrength, MaxRumbleStrength)
	scale := func(v uint16) uint8 { return uint8(math.Round(float64(v) / 65535 * 255 * k)) }
	return scale(low), scale(high)
}
