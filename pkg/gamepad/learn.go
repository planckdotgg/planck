package gamepad

import (
	"fmt"
	"slices"
)

// ParseInput reads an Input by its SDL mapping name, such as "a" or
// "lefttrigger".
func ParseInput(name string) (Input, error) {
	if i := slices.Index(sdlNames[:], name); i >= 0 {
		return Input(i), nil
	}
	return 0, fmt.Errorf("gamepad: unknown input %q", name)
}

// learnTravel is how far an axis must move from where it rested, on the
// -1..1 scale, before Learn takes it: half its travel, past any drift.
const learnTravel = 0.5

// Learn finds the control the user moved between rest and now to bind to
// target, for mapping a controller by pressing what should drive each
// input. A stick input takes only an axis, inverted when it moved toward
// negative: sticks are learned by pushing right on X and down on Y, SDL's
// positive directions. A trigger takes an axis the same way, or a button.
// A button takes a button, one hat direction, or the half of an axis it
// moved into.
func Learn(rest, now *RawState, target Input) (Binding, bool) {
	stick := isStick(target)
	trigger := target == InputLeftTrigger || target == InputRightTrigger
	if !stick {
		for i := range min(len(rest.Buttons), len(now.Buttons)) {
			if now.Buttons[i] && !rest.Buttons[i] {
				return Binding{Kind: SourceButton, Index: i}, true
			}
		}
	}
	if !stick && !trigger {
		for i := range min(len(rest.Hats), len(now.Hats)) {
			added := now.Hats[i] &^ rest.Hats[i]
			for _, d := range [...]uint8{HatUp, HatRight, HatDown, HatLeft} {
				if added&d != 0 {
					return Binding{Kind: SourceHat, Index: i, HatMask: d}, true
				}
			}
		}
	}
	for i := range min(len(rest.Axes), len(now.Axes)) {
		d := now.Axes[i].Norm() - rest.Axes[i].Norm()
		if d < learnTravel && d > -learnTravel {
			continue
		}
		b := Binding{Kind: SourceAxis, Index: i}
		switch {
		case stick || trigger:
			b.Invert = d < 0
		case d > 0:
			b.Range = RangePositive
		default:
			b.Range = RangeNegative
		}
		return b, true
	}
	return Binding{}, false
}
