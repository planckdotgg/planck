package theme

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestRegisterFonts(t *testing.T) {
	if err := RegisterFonts(); err != nil {
		t.Fatal(err)
	}
	if err := RegisterFonts(); err != nil {
		t.Fatalf("second call: %v", err)
	}
	var registered []string
	for _, f := range ui.FontFamilies() {
		if f.Registered {
			registered = append(registered, f.Name)
		}
	}
	for _, want := range []string{FontUI, FontDisplay, FontMono} {
		if !slices.Contains(registered, want) {
			t.Errorf("family %q not registered; registered: %q", want, registered)
		}
	}
}

func TestMonoFontIsMonospaced(t *testing.T) {
	if err := RegisterFonts(); err != nil {
		t.Fatal(err)
	}
	for _, f := range ui.FontFamilies() {
		if f.Name == FontMono && !f.Monospaced {
			t.Fatalf("%s is not monospaced: live digits would jitter", FontMono)
		}
	}
}

func TestIconsParsed(t *testing.T) {
	for name, s := range map[string]*ui.SVG{"Mark": Mark, "IconHosts": IconHosts, "IconPairing": IconPairing,
		"IconSetting": IconSetting, "IconHosting": IconHosting, "IconChevron": IconChevron, "IconEdit": IconEdit} {
		w, h := ui.ImageSize(s)
		if w <= 0 || h <= 0 {
			t.Errorf("%s has size %vx%v", name, w, h)
		}
	}
	if w, h := ui.ImageSize(Mark); w != 22 || h != 34 {
		t.Errorf("rail mark is %vx%v, want 22x34", w, h)
	}
}

func TestLauncherIconSizes(t *testing.T) {
	want := map[string]struct {
		s    *ui.SVG
		size float32
	}{
		"IconStatisticsNav": {IconStatisticsNav, 24}, "IconStatistics": {IconStatistics, 16},
		"IconPrompt": {IconPrompt, 16}, "IconSearch": {IconSearch, 16}, "IconReset": {IconReset, 16},
		"IconLock": {IconLock, 16}, "IconShieldCheck": {IconShieldCheck, 16},
		"IconWarning": {IconWarning, 16}, "IconInfo": {IconInfo, 16},
	}
	for name, i := range want {
		if w, h := ui.ImageSize(i.s); w != i.size || h != i.size {
			t.Errorf("%s is %vx%v, want %vx%[4]v", name, w, h, i.size)
		}
	}
}

// TestTokensMatchJSON checks the generated Go roles against tokens.json,
// the single source, so a hand edit or a stale copy shows up.
func TestTokensMatchJSON(t *testing.T) {
	data, err := os.ReadFile("../../../assets/tokens.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Color map[string]map[string]json.RawMessage `json:"color"`
		Role  map[string]json.RawMessage            `json:"role"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	value := func(raw json.RawMessage) string {
		var v struct {
			Value string `json:"$value"`
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			return ""
		}
		return v.Value
	}
	resolve := func(v string) string {
		if !strings.HasPrefix(v, "{color.") {
			return v
		}
		path := strings.Split(strings.Trim(v, "{}"), ".")
		return value(doc.Color[path[1]][path[2]])
	}
	roles := map[string]ui.Color{
		"ground": RoleGround, "well": RoleWell, "surface": RoleSurface, "raised": RoleRaised,
		"hover": RoleHover, "border": RoleBorder, "border-strong": RoleBorderStrong,
		"text": RoleText, "text-2": RoleText2, "text-3": RoleText3, "accent": RoleAccent,
		"accent-hover": RoleAccentHover, "accent-pressed": RoleAccentPressed, "on-accent": RoleOnAccent,
		"focus": RoleFocus, "direct": RoleDirect, "relay": RoleRelay, "fault": RoleFault,
		"control-edge": RoleControlEdge,
	}
	for name, got := range roles {
		raw, ok := doc.Role[name]
		if !ok {
			t.Errorf("role %q missing from tokens.json", name)
			continue
		}
		hex := resolve(value(raw))
		if want := ui.Hex(hex); got != want {
			t.Errorf("role %q = %v, tokens.json says %s", name, got, hex)
		}
	}
}

func TestPlanckTheme(t *testing.T) {
	th := Planck()
	if !th.Dark || th.Background != RoleGround || th.Accent != RoleAccent || th.AccentText != RoleOnAccent {
		t.Fatalf("theme roles: %+v", th)
	}
	if th.Font != FontUI || th.FontSize != TextBody {
		t.Fatalf("theme type: %q %v", th.Font, th.FontSize)
	}
}
