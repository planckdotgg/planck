package gpusurface

// fitRect places a fw x fh frame whole in a dw x dh drawable, centred on
// whole pixels: the scale x, y and offset x, y in clip space that take the
// drawable's quad to it, and whether it leaves bars.
func fitRect(fw, fh, dw, dh uintptr) (fit [4]float32, bars bool) {
	if fw == 0 || fh == 0 || dw == 0 || dh == 0 || fw*dh == fh*dw {
		return [4]float32{1, 1, 0, 0}, false
	}
	vw, vh := dw, dh
	if fw*dh > fh*dw {
		vh = (dw*fh + fw/2) / fw
	} else {
		vw = (dh*fw + fh/2) / fh
	}
	x0, y0 := (dw-vw)/2, (dh-vh)/2
	w, h := float32(dw), float32(dh)
	return [4]float32{float32(vw) / w, float32(vh) / h, float32(2*x0+vw)/w - 1, 1 - float32(2*y0+vh)/h}, vw != dw || vh != dh
}
