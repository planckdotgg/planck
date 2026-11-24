package ui

import (
	mui "github.com/egoist/mygo/ui"
	"github.com/planckdotgg/planck/internal/ui/theme"
)

func indexOf[T comparable](list []T, v T) int {
	for i, x := range list {
		if x == v {
			return i
		}
	}
	return -1
}

// pickFrom binds a segmented control's index to *v: a choice the list
// lacks shows no segment chosen.
func pickFrom[T comparable](c *mui.Context, name string, list []T, labels []string, v *T) {
	i := indexOf(list, *v)
	if segmented(c, name, &i, labels...) && i >= 0 {
		*v = list[i]
	}
}

// stage is one part of glass-to-glass, colored along the blackbody
// spectrum in pipeline order.
type stage struct {
	title string
	ms    float64
	col   mui.Color
}

func stages(t Telemetry) [5]stage {
	return [5]stage{
		{"Capture", t.CaptureMs, theme.SpectrumK2000},
		{"Encode", t.EncodeMs, theme.SpectrumK3000},
		{"Network", t.NetMs, theme.SpectrumK4000},
		{"Decode", t.DecodeMs, theme.SpectrumK6500},
		{"Present", t.PresentMs, theme.SpectrumK10000},
	}
}

// stageBar draws each stage as a slice as wide as its share of the stages'
// sum.
func stageBar(c *mui.Context, t Telemetry) {
	st := stages(t)
	var sum float64
	for _, s := range st {
		sum += s.ms
	}
	mui.Box(c).FillWidth().Height(6).Label("Stage times").Draw(func(p *mui.Painter, r mui.Rect) {
		if sum <= 0 {
			p.Fill(r, theme.RoleBorder, 2)
			return
		}
		const gap = 3
		avail := r.W - gap*float32(len(st)-1)
		x := r.X
		for _, s := range st {
			if s.ms <= 0 {
				continue
			}
			w := avail * float32(s.ms/sum)
			p.Fill(mui.Rect{X: x, Y: r.Y, W: w, H: r.H}, s.col, 2)
			x += w + gap
		}
	})
}

func hudReading(c *mui.Context, v, unit string) {
	mui.Row(c).AlignItems(mui.Baseline).Gap(3).Children(func() {
		mono(c, v).FontWeight(500)
		mono(c, unit).FontSize(theme.TextCaption).TextColor(theme.RoleText3)
	})
}

func (a *App) streamScreen(c *mui.Context, tel Telemetry, connecting string) {
	well := mui.Box(c).Fill()
	if !tel.Connected || a.streamView == nil {
		well.Background(theme.RoleWell)
	}
	well.Children(func() {
		if !tel.Connected {
			mui.Column(c).Fill().AlignItems(mui.Center).Justify(mui.Center).Gap(theme.Space4).Children(func() {
				sliceMeter(c, 1, 4, theme.RoleAccent)
				heading(c, "Connecting to "+connecting, theme.TextDisplay)
				if button(c, buttonGhost, "Cancel", false).Clicked() {
					a.cancelConnect()
				}
			})
			return
		}
		if a.streamView != nil {
			mui.Box(c).Fill().Children(func() { a.streamView(c) })
		}
		a.overlayView(c, well, tel)
	})
}
