// Code generated from planck-design/tokens.json by gen_tokens.py. DO NOT EDIT.

package theme

import "github.com/egoist/mygo/ui"

// Colour primitives.
var (
	Carbon950         = ui.Hex("#07080A")
	Carbon900         = ui.Hex("#0E0F12")
	Carbon850         = ui.Hex("#14161A")
	Carbon800         = ui.Hex("#1B1D22")
	Carbon750         = ui.Hex("#22252B")
	Carbon700         = ui.Hex("#2C3038")
	Carbon600         = ui.Hex("#3A3F49")
	Carbon500         = ui.Hex("#5A606C")
	Carbon400         = ui.Hex("#6E7480")
	Bone50            = ui.Hex("#F2EFEA")
	Bone300           = ui.Hex("#B6B8BE")
	Bone400           = ui.Hex("#8C919B")
	BoneLightGround   = ui.Hex("#F6F5F2")
	BoneLightSurface  = ui.Hex("#FFFFFF")
	BoneLightBorder   = ui.Hex("#DAD8D3")
	BoneLightText     = ui.Hex("#0E0F12")
	BoneLightText2    = ui.Hex("#4E535C")
	Ember300          = ui.Hex("#FFB27A")
	Ember400          = ui.Hex("#FF9447")
	Ember500          = ui.Hex("#FF7A1F")
	Ember600          = ui.Hex("#E8650C")
	Ember700          = ui.Hex("#A84603")
	EmberInk          = ui.Hex("#1A0B02")
	SignalDirect      = ui.Hex("#4CD69B")
	SignalRelay       = ui.Hex("#F2C94C")
	SignalFault       = ui.Hex("#FF5A5A")
	SignalDirectLight = ui.Hex("#13804F")
	SignalRelayLight  = ui.Hex("#8A6400")
	SignalFaultLight  = ui.Hex("#C42B2B")
	SpectrumK2000     = ui.Hex("#FF8912")
	SpectrumK3000     = ui.Hex("#FFB46B")
	SpectrumK4000     = ui.Hex("#FFD1A3")
	SpectrumK6500     = ui.Hex("#FFF9FD")
	SpectrumK10000    = ui.Hex("#C9D9FF")
)

// Roles. Widgets use only these.
var (
	RoleGround        = Carbon900
	RoleWell          = Carbon950
	RoleSurface       = Carbon850
	RoleRaised        = Carbon800
	RoleHover         = Carbon750
	RoleBorder        = Carbon700
	RoleBorderStrong  = Carbon600
	RoleText          = Bone50
	RoleText2         = Bone300
	RoleText3         = Bone400
	RoleAccent        = Ember500
	RoleAccentHover   = Ember400
	RoleAccentPressed = Ember600
	RoleOnAccent      = EmberInk
	RoleFocus         = Ember300
	RoleDirect        = SignalDirect
	RoleRelay         = SignalRelay
	RoleFault         = SignalFault
	RoleControlEdge   = Carbon400
)

// Space, size and radius in px.
const (
	SpaceHair      float32 = 2
	Space1         float32 = 4
	Space2         float32 = 8
	Space3         float32 = 12
	Space4         float32 = 16
	Space5         float32 = 24
	Space6         float32 = 32
	Space7         float32 = 48
	SpaceSection   float32 = 96
	SpaceSectionLg float32 = 160
	SizeTargetMin  float32 = 24
	SizeControl    float32 = 32
	SizeControlLg  float32 = 40
	SizeRow        float32 = 64
	SizeIcon       float32 = 16
	SizeIconLg     float32 = 20
	SizeSidebar    float32 = 224
	SizeContainer  float32 = 1200
	SizeRail       float32 = 88
	SizeRailButton float32 = 56
	SizeRailIcon   float32 = 24
	RadiusXs       float32 = 3
	RadiusSm       float32 = 6
	RadiusMd       float32 = 10
	RadiusPill     float32 = 999
)

// Type: app scale, size and line height in px.
const (
	TextMicro      float32 = 9
	LeadingMicro   float32 = 12
	TextMini       float32 = 10
	LeadingMini    float32 = 14
	TextCaption    float32 = 11
	LeadingCaption float32 = 16
	TextBody       float32 = 13
	LeadingBody    float32 = 20
	TextTitle      float32 = 16
	LeadingTitle   float32 = 24
	TextHeading    float32 = 20
	LeadingHeading float32 = 28
	TextDisplay    float32 = 25
	LeadingDisplay float32 = 32
	TextReadout    float32 = 31
	LeadingReadout float32 = 36
	TextPin        float32 = 39
	LeadingPin     float32 = 44
)

// Font families as registered with ui.RegisterFont from the static instances in fonts/static.
const (
	FontDisplay = "Archivo Expanded"
	FontUI      = "Archivo"
	FontMono    = "Martian Mono"
)

// Motion, in milliseconds.
const (
	DurFast    float32 = 90
	DurBase    float32 = 160
	DurExit    float32 = 110
	DurStagger float32 = 18
)
