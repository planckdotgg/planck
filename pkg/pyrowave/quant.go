package pyrowave

import (
	"math"
)

const maxScaleExp = 4

// Block32x32Scale decodes a block header's quant_code: 3-bit mantissa,
// 5-bit exponent, a value in (0, 16).
func Block32x32Scale(code uint8) float32 {
	e := maxScaleExp - int(code>>3)
	m := int(code & 7)
	return float32(float64(8+m) * math.Ldexp(1, 20+e) / (8 * 1024 * 1024))
}

// EncodeQuant is the quant_code whose Block32x32Scale is at most scale and
// shares its exponent: the inverse of Block32x32Scale for the encoder.
func EncodeQuant(scale float32) uint8 {
	v := math.Float32bits(scale)
	e := int(v>>23&0xff) - 127 - maxScaleExp
	m := int(v >> 20 & 7)
	e = -e
	e = min(max(e, 0), 31)
	return uint8(e<<3 | m)
}

// Block8x8Scale decodes the high nibble of an 8x8 block's QScale byte.
func Block8x8Scale(code uint8) float32 { return float32(code)/8 + 0.25 }

// quantScaleIdentity is the QScale code whose Block8x8Scale is 1.
const quantScaleIdentity = 6

// QuantCode8x8 picks an 8x8 block's scale code from the magnitude of its
// largest quantized coefficient, so that magnitude lands just below a power
// of two, and returns the code and the factor to multiply coefficients by.
func QuantCode8x8(maxAbs float32) (uint8, float32) {
	if maxAbs < 1 {
		return quantScaleIdentity, 1
	}
	_, e := math.Frexp(float64(maxAbs - 0.25))
	target := float32(math.Ldexp(1, e)) - 0.25
	inv := maxAbs / target
	code := uint8(math.Ceil(float64((inv - 0.25) * 8)))
	return code, 1 / Block8x8Scale(code)
}

// quantResolutionBits is the initial quantizer: a flat spectrum after noise
// power normalization (the CDF 9/7 low pass gains 6 dB a level).
func quantResolutionBits(level, component, kind int) int {
	bits := 8
	switch {
	case kind == 0:
		bits += 2
	case kind < 3:
		bits++
	}
	bits += level
	if component != 0 {
		bits--
	}
	return bits
}

// QuantResolution is the factor band coefficients are scaled by before
// truncation to integers, rounded to what quant_code can signal.
func QuantResolution(level, component, kind int) float32 {
	r := min(float32(4096), float32(int(1)<<quantResolutionBits(level, component, kind)))
	return 1 / Block32x32Scale(EncodeQuant(1/r))
}

// RDOScale weights a band's squared coefficient error by a contrast
// sensitivity model (96 DPI, viewing distance of one screen height), so rate
// control spends bits where the eye sees them.
func RDOScale(level, component, kind int, chroma420 bool) float32 {
	hm, vm := 0.25, 0.25
	if kind&1 != 0 {
		hm = 0.75
	}
	if kind&2 != 0 {
		vm = 0.75
	}
	const nyquist = 0.34 * 1.0 * 96.0
	cpd := math.Sqrt(hm*hm+vm*vm) * nyquist * math.Exp2(-float64(level))
	cpd = max(cpd, 8)
	csf := 2.6 * (0.0192 + 0.114*cpd) * math.Exp(-math.Pow(0.114*cpd, 1.1))
	if component != 0 && level != Levels-1 && chroma420 {
		csf *= 0.6
	}
	res := float64(int(1) << quantResolutionBits(level, component, kind))
	w := csf * res
	return float32(w * w / 256)
}
