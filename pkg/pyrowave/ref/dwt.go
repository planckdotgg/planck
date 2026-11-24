// Package ref is a CPU PyroWave encoder and decoder in float32, the
// reference the GPU kernels are tested against. It follows the same steps
// as the shaders, in the same order, so their outputs agree up to floating
// point rounding.
package ref

// CDF 9/7 lifting constants (JPEG 2000, ITU-T T.800 F.4.8.2).
const (
	alpha float32 = -1.586134342059924
	beta  float32 = -0.052980118572961
	gamma float32 = 0.882911075530934
	delta float32 = 0.443506852043971
	kGain float32 = 1.230174104914001
	kInv  float32 = 1 / kGain
)

// mirror extends a signal of even length n symmetrically about its first and
// last samples (whole-sample symmetry), as the GPU's mirrored sampling does.
func mirror(i, n int) int {
	if i < 0 {
		return -i
	}
	if i >= n {
		return 2*n - 2 - i
	}
	return i
}

func lift(x []float32, get func(int) float32, parity int, c float32, n int) {
	for i := parity; i < n; i += 2 {
		x[i] += c * (get(i-1) + get(i+1))
	}
}

func forward1D(x []float32) {
	n := len(x)
	get := func(i int) float32 { return x[mirror(i, n)] }
	lift(x, get, 1, alpha, n)
	lift(x, get, 0, beta, n)
	lift(x, get, 1, gamma, n)
	lift(x, get, 0, delta, n)
	for i := 0; i < n; i += 2 {
		x[i] *= kInv
		x[i+1] *= kGain
	}
}

func inverse1D(x []float32) {
	n := len(x)
	for i := 0; i < n; i += 2 {
		x[i] *= kGain
		x[i+1] *= kInv
	}
	get := func(i int) float32 { return x[mirror(i, n)] }
	lift(x, get, 0, -delta, n)
	lift(x, get, 1, -gamma, n)
	lift(x, get, 0, -beta, n)
	lift(x, get, 1, -alpha, n)
}

// forward2D splits a w x h plane into LL, HL, LH and HH bands of w/2 x h/2.
func forward2D(src []float32, w, h int, bands *[4][]float32) {
	tmp := make([]float32, w*h)
	copy(tmp, src)
	for y := 0; y < h; y++ {
		forward1D(tmp[y*w : (y+1)*w])
	}
	col := make([]float32, h)
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			col[y] = tmp[y*w+x]
		}
		forward1D(col)
		for y := 0; y < h; y++ {
			tmp[y*w+x] = col[y]
		}
	}
	hw, hh := w/2, h/2
	for k := range bands {
		bands[k] = make([]float32, hw*hh)
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			kind := x&1 | (y&1)<<1
			bands[kind][(y/2)*hw+x/2] = tmp[y*w+x]
		}
	}
}

// inverse2D rebuilds a w x h plane from its four bands.
func inverse2D(bands *[4][]float32, w, h int) []float32 {
	hw := w / 2
	out := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			out[y*w+x] = bands[x&1|(y&1)<<1][(y/2)*hw+x/2]
		}
	}
	col := make([]float32, h)
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			col[y] = out[y*w+x]
		}
		inverse1D(col)
		for y := 0; y < h; y++ {
			out[y*w+x] = col[y]
		}
	}
	for y := 0; y < h; y++ {
		inverse1D(out[y*w : (y+1)*w])
	}
	return out
}
