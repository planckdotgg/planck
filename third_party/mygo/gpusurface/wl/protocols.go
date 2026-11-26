//go:build linux && !android && (amd64 || arm64)

package wl

// Interfaces of the protocols outside the core that libwayland-client
// does not export, from wayland-protocols 1.36. They are filled by Load.
var (
	WpViewporter                         = &Interface{}
	WpViewport                           = &Interface{}
	ZwpLinuxDmabufV1                     = &Interface{}
	ZwpLinuxBufferParamsV1               = &Interface{}
	ZwpLinuxDmabufFeedbackV1             = &Interface{}
	WpPresentation                       = &Interface{}
	WpPresentationFeedback               = &Interface{}
	ZwpPointerConstraintsV1              = &Interface{}
	ZwpLockedPointerV1                   = &Interface{}
	ZwpConfinedPointerV1                 = &Interface{}
	ZwpRelativePointerManagerV1          = &Interface{}
	ZwpRelativePointerV1                 = &Interface{}
	ZwpKeyboardShortcutsInhibitManagerV1 = &Interface{}
	ZwpKeyboardShortcutsInhibitorV1      = &Interface{}
)

func defineProtocols() {
	define(WpViewporter, "wp_viewporter", 1,
		[]Message{
			{"destroy", "", nil},
			{"get_viewport", "no", []*Interface{WpViewport, Core("wl_surface")}},
		}, nil)
	define(WpViewport, "wp_viewport", 1,
		[]Message{
			{"destroy", "", nil},
			{"set_source", "ffff", []*Interface{nil, nil, nil, nil}},
			{"set_destination", "ii", []*Interface{nil, nil}},
		}, nil)
	define(ZwpLinuxDmabufV1, "zwp_linux_dmabuf_v1", 5,
		[]Message{
			{"destroy", "", nil},
			{"create_params", "n", []*Interface{ZwpLinuxBufferParamsV1}},
			{"get_default_feedback", "4n", []*Interface{ZwpLinuxDmabufFeedbackV1}},
			{"get_surface_feedback", "4no", []*Interface{ZwpLinuxDmabufFeedbackV1, Core("wl_surface")}},
		},
		[]Message{
			{"format", "u", []*Interface{nil}},
			{"modifier", "3uuu", []*Interface{nil, nil, nil}},
		})
	define(ZwpLinuxBufferParamsV1, "zwp_linux_buffer_params_v1", 5,
		[]Message{
			{"destroy", "", nil},
			{"add", "huuuuu", []*Interface{nil, nil, nil, nil, nil, nil}},
			{"create", "iiuu", []*Interface{nil, nil, nil, nil}},
			{"create_immed", "2niiuu", []*Interface{Core("wl_buffer"), nil, nil, nil, nil}},
		},
		[]Message{
			{"created", "n", []*Interface{Core("wl_buffer")}},
			{"failed", "", nil},
		})
	define(ZwpLinuxDmabufFeedbackV1, "zwp_linux_dmabuf_feedback_v1", 5,
		[]Message{
			{"destroy", "", nil},
		},
		[]Message{
			{"done", "", nil},
			{"format_table", "hu", []*Interface{nil, nil}},
			{"main_device", "a", []*Interface{nil}},
			{"tranche_done", "", nil},
			{"tranche_target_device", "a", []*Interface{nil}},
			{"tranche_formats", "a", []*Interface{nil}},
			{"tranche_flags", "u", []*Interface{nil}},
		})
	define(WpPresentation, "wp_presentation", 1,
		[]Message{
			{"destroy", "", nil},
			{"feedback", "on", []*Interface{Core("wl_surface"), WpPresentationFeedback}},
		},
		[]Message{
			{"clock_id", "u", []*Interface{nil}},
		})
	define(WpPresentationFeedback, "wp_presentation_feedback", 1, nil,
		[]Message{
			{"sync_output", "o", []*Interface{Core("wl_output")}},
			{"presented", "uuuuuuu", []*Interface{nil, nil, nil, nil, nil, nil, nil}},
			{"discarded", "", nil},
		})
	define(ZwpPointerConstraintsV1, "zwp_pointer_constraints_v1", 1,
		[]Message{
			{"destroy", "", nil},
			{"lock_pointer", "noo?ou", []*Interface{ZwpLockedPointerV1, Core("wl_surface"), Core("wl_pointer"), Core("wl_region"), nil}},
			{"confine_pointer", "noo?ou", []*Interface{ZwpConfinedPointerV1, Core("wl_surface"), Core("wl_pointer"), Core("wl_region"), nil}},
		}, nil)
	define(ZwpLockedPointerV1, "zwp_locked_pointer_v1", 1,
		[]Message{
			{"destroy", "", nil},
			{"set_cursor_position_hint", "ff", []*Interface{nil, nil}},
			{"set_region", "?o", []*Interface{Core("wl_region")}},
		},
		[]Message{
			{"locked", "", nil},
			{"unlocked", "", nil},
		})
	define(ZwpConfinedPointerV1, "zwp_confined_pointer_v1", 1,
		[]Message{
			{"destroy", "", nil},
			{"set_region", "?o", []*Interface{Core("wl_region")}},
		},
		[]Message{
			{"confined", "", nil},
			{"unconfined", "", nil},
		})
	define(ZwpRelativePointerManagerV1, "zwp_relative_pointer_manager_v1", 1,
		[]Message{
			{"destroy", "", nil},
			{"get_relative_pointer", "no", []*Interface{ZwpRelativePointerV1, Core("wl_pointer")}},
		}, nil)
	define(ZwpRelativePointerV1, "zwp_relative_pointer_v1", 1,
		[]Message{
			{"destroy", "", nil},
		},
		[]Message{
			{"relative_motion", "uuffff", []*Interface{nil, nil, nil, nil, nil, nil}},
		})
	define(ZwpKeyboardShortcutsInhibitManagerV1, "zwp_keyboard_shortcuts_inhibit_manager_v1", 1,
		[]Message{
			{"destroy", "", nil},
			{"inhibit_shortcuts", "noo", []*Interface{ZwpKeyboardShortcutsInhibitorV1, Core("wl_surface"), Core("wl_seat")}},
		}, nil)
	define(ZwpKeyboardShortcutsInhibitorV1, "zwp_keyboard_shortcuts_inhibitor_v1", 1,
		[]Message{
			{"destroy", "", nil},
		},
		[]Message{
			{"active", "", nil},
			{"inactive", "", nil},
		})
}

// Request opcodes, by interface, of the requests this package's users
// send. Destructors are opcode 0 unless named here.
const (
	CompositorCreateSurface = 0
	CompositorCreateRegion  = 1

	SubcompositorGetSubsurface = 1

	SubsurfaceSetPosition = 1
	SubsurfacePlaceAbove  = 2
	SubsurfacePlaceBelow  = 3
	SubsurfaceSetDesync   = 5

	SurfaceAttach         = 1
	SurfaceFrame          = 3
	SurfaceSetOpaque      = 4
	SurfaceSetInputRegion = 5
	SurfaceCommit         = 6
	SurfaceSetBufferScale = 8
	SurfaceDamageBuffer   = 9

	RegionAdd = 1

	ShmCreatePool    = 0
	ShmPoolCreateBuf = 0
	ShmPoolDestroy   = 1
	SeatGetPointer   = 0
	SeatGetKeyboard  = 1
	SeatRelease      = 3 // since 5
	PointerSetCursor = 0
	PointerRelease   = 1 // since 3
	KeyboardRelease  = 0 // since 3

	ViewporterGetViewport = 1
	ViewportSetSource     = 1
	ViewportSetDest       = 2

	DmabufCreateParams = 1
	ParamsAdd          = 1
	ParamsCreate       = 2

	PresentationFeedback = 1

	ConstraintsLockPointer    = 1
	ConstraintsConfinePointer = 2
	LockedSetCursorHint       = 1

	RelativeGetPointer = 1

	InhibitShortcuts = 1
)

// LifetimePersistent keeps a pointer constraint across the pointer leaving
// and coming back; LifetimeOneshot ends it then.
const (
	LifetimeOneshot    = 1
	LifetimePersistent = 2
)
