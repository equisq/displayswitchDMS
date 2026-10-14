package displays

import (
	"errors"
	"sort"
)

var (
	ErrNoDisplays = errors.New("no display connected")
	ErrNoInternal = errors.New("no built-in display")
	ErrNoExternal = errors.New("no external display")
	ErrNeedTwo    = errors.New("at least two displays are needed")
	ErrNoWlMirror = errors.New("wl-mirror is not installed")
)

// Caps describes optional abilities of the host.
type Caps struct {
	WlMirror bool `json:"wlMirror"`
}

// MirrorPlan says what wl-mirror must show where.
type MirrorPlan struct {
	Source  string   `json:"source"`
	Targets []string `json:"targets"`
}

// Plan is the outcome of asking for a mode with a given set of displays.
type Plan struct {
	Requested Mode
	Effective Mode // what we will really end up with
	Fallback  bool // Effective differs from Requested because of missing hardware
	Enable    []string
	Disable   []string
	Mirror    *MirrorPlan
}

// BuildPlan decides the desired enabled/disabled state of every output.
//
// Invariant: Enable is never empty, so a plan can never black out the machine.
func BuildPlan(req Mode, ds []Display, caps Caps) (Plan, error) {
	if len(ds) == 0 {
		return Plan{}, ErrNoDisplays
	}
	in, ex := Internals(ds), Externals(ds)
	p := Plan{Requested: req, Effective: req}

	single := func() Mode {
		if len(in) > 0 {
			return ModeInternal
		}
		return ModeExternal
	}

	switch req {
	case ModeInternal:
		if len(in) == 0 {
			return Plan{}, ErrNoInternal
		}
		p.Enable, p.Disable = names(in), names(ex)

	case ModeExternal:
		if len(ex) == 0 {
			if len(in) == 0 {
				return Plan{}, ErrNoDisplays
			}
			// External monitors are gone: fall back to the built-in screen.
			p.Effective, p.Fallback = ModeInternal, true
			p.Enable = names(in)
			break
		}
		p.Enable, p.Disable = names(ex), names(in)

	case ModeExtend:
		p.Enable = names(ds)
		if len(ds) < 2 {
			p.Effective, p.Fallback = single(), true
		}

	case ModeMirror:
		if len(ds) < 2 {
			p.Effective, p.Fallback = single(), true
			p.Enable = names(ds)
			break
		}
		if !caps.WlMirror {
			return Plan{}, ErrNoWlMirror
		}
		p.Enable = names(ds)
		src := mirrorSource(ds)
		mp := &MirrorPlan{Source: src.Name}
		for _, d := range ds {
			if d.Name != src.Name {
				mp.Targets = append(mp.Targets, d.Name)
			}
		}
		p.Mirror = mp

	default:
		return Plan{}, errors.New("unknown mode " + string(req))
	}
	return p, nil
}

// mirrorSource picks the display whose picture is cloned: the built-in panel
// when there is one, otherwise the left-most enabled display.
func mirrorSource(ds []Display) Display {
	if in := Internals(ds); len(in) > 0 {
		return in[0]
	}
	cand := enabled(ds)
	if len(cand) == 0 {
		cand = append(cand, ds...)
	}
	sort.SliceStable(cand, func(i, j int) bool {
		if cand[i].X != cand[j].X {
			return cand[i].X < cand[j].X
		}
		return cand[i].Name < cand[j].Name
	})
	return cand[0]
}

// Diff returns the outputs that must still be enabled / disabled.
func (p Plan) Diff(ds []Display) (toEnable, toDisable []string) {
	state := map[string]bool{}
	for _, d := range ds {
		state[d.Name] = d.Enabled
	}
	for _, n := range p.Enable {
		if !state[n] {
			toEnable = append(toEnable, n)
		}
	}
	for _, n := range p.Disable {
		if state[n] {
			toDisable = append(toDisable, n)
		}
	}
	return
}

// Availability tells the UI whether a mode can be picked right now.
type Availability struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"` // no_internal | no_external | need_two | no_wl_mirror
}

func Availabilities(ds []Display, caps Caps) map[Mode]Availability {
	in, ex := Internals(ds), Externals(ds)
	m := map[Mode]Availability{
		ModeInternal: {Available: len(in) > 0},
		ModeExternal: {Available: len(ex) > 0},
		ModeExtend:   {Available: len(ds) >= 2},
		ModeMirror:   {Available: len(ds) >= 2 && caps.WlMirror},
	}
	set := func(k Mode, reason string) {
		if a := m[k]; !a.Available {
			a.Reason = reason
			m[k] = a
		}
	}
	set(ModeInternal, "no_internal")
	set(ModeExternal, "no_external")
	set(ModeExtend, "need_two")
	if len(ds) < 2 {
		set(ModeMirror, "need_two")
	} else {
		set(ModeMirror, "no_wl_mirror")
	}
	return m
}
