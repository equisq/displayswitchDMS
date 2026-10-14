// Package displays holds the pure logic behind the Super+P switcher: how
// outputs are classified, what each of the four modes means for a given set of
// connected displays, and which outputs must be switched on or off to get there.
package displays

import (
	"fmt"
	"sort"
	"strings"

	"github.com/equisq/displayswitchDMS/backend/internal/niri"
)

// Mode is one of the four layouts offered by the switcher.
type Mode string

const (
	ModeInternal Mode = "internal" // built-in (laptop) screen only
	ModeExternal Mode = "external" // external monitors only, built-in off
	ModeMirror   Mode = "mirror"   // same picture everywhere (wl-mirror)
	ModeExtend   Mode = "extend"   // every display on, side by side
)

// AllModes is the display order of the popup.
var AllModes = []Mode{ModeInternal, ModeExternal, ModeMirror, ModeExtend}

func (m Mode) Valid() bool {
	for _, v := range AllModes {
		if m == v {
			return true
		}
	}
	return false
}

// DefaultInternalPrefixes are connector-name prefixes of built-in panels.
var DefaultInternalPrefixes = []string{"eDP", "LVDS", "DSI"}

// Display is one connected output, enriched for the UI.
type Display struct {
	Name     string `json:"name"`     // connector name, e.g. "HDMI-A-1"
	Label    string `json:"label"`    // "Dell U2720Q"
	Identity string `json:"-"`        // stable across ports: make|model|serial
	Internal bool   `json:"internal"` // built-in panel
	Enabled  bool   `json:"enabled"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	X        int    `json:"x"`
	Y        int    `json:"y"`
}

// IsInternalName reports whether a connector name looks like a built-in panel.
func IsInternalName(name string, prefixes []string) bool {
	if len(prefixes) == 0 {
		prefixes = DefaultInternalPrefixes
	}
	up := strings.ToUpper(name)
	for _, p := range prefixes {
		p = strings.ToUpper(strings.TrimSpace(p))
		if p != "" && strings.HasPrefix(up, p) {
			return true
		}
	}
	return false
}

// FromOutputs converts niri outputs to Displays, sorted internal-first then by name.
func FromOutputs(outs []niri.Output, internalPrefixes []string) []Display {
	ds := make([]Display, 0, len(outs))
	seen := map[string]int{}
	for _, o := range outs {
		d := Display{
			Name:     o.Name,
			Internal: IsInternalName(o.Name, internalPrefixes),
			Enabled:  o.Enabled(),
		}
		label := strings.TrimSpace(strings.TrimSpace(o.Make) + " " + strings.TrimSpace(o.Model))
		if label == "" || strings.EqualFold(label, "unknown unknown") {
			label = o.Name
		}
		d.Label = label

		switch {
		case o.Logical != nil:
			d.Width, d.Height, d.X, d.Y = o.Logical.Width, o.Logical.Height, o.Logical.X, o.Logical.Y
		case o.CurrentMode != nil && *o.CurrentMode >= 0 && *o.CurrentMode < len(o.Modes):
			m := o.Modes[*o.CurrentMode]
			d.Width, d.Height = m.Width, m.Height
		default:
			for _, m := range o.Modes {
				if m.IsPreferred {
					d.Width, d.Height = m.Width, m.Height
					break
				}
			}
		}

		serial := ""
		if o.Serial != nil {
			serial = *o.Serial
		}
		id := o.Make + "|" + o.Model + "|" + serial
		if serial == "" {
			// no serial: the connector is the best discriminator we have
			id += "|" + o.Name
		}
		seen[id]++
		if n := seen[id]; n > 1 {
			id = fmt.Sprintf("%s#%d", id, n)
		}
		d.Identity = id
		ds = append(ds, d)
	}
	sort.SliceStable(ds, func(i, j int) bool {
		if ds[i].Internal != ds[j].Internal {
			return ds[i].Internal
		}
		return ds[i].Name < ds[j].Name
	})
	return ds
}

// Signature identifies the set of connected displays independently of the
// ports they are plugged into and of whether they are currently enabled.
func Signature(ds []Display) string {
	ids := make([]string, 0, len(ds))
	for _, d := range ds {
		ids = append(ids, d.Identity)
	}
	sort.Strings(ids)
	return strings.Join(ids, "+")
}

func Internals(ds []Display) []Display { return filter(ds, true) }
func Externals(ds []Display) []Display { return filter(ds, false) }

func filter(ds []Display, internal bool) []Display {
	var out []Display
	for _, d := range ds {
		if d.Internal == internal {
			out = append(out, d)
		}
	}
	return out
}

func enabled(ds []Display) []Display {
	var out []Display
	for _, d := range ds {
		if d.Enabled {
			out = append(out, d)
		}
	}
	return out
}

func names(ds []Display) []string {
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.Name)
	}
	return out
}

// Detect works out which mode the current output state corresponds to.
// It returns "" when no output is enabled.
func Detect(ds []Display, mirrorActive bool) Mode {
	en := enabled(ds)
	if len(en) == 0 {
		return ""
	}
	in, ex := len(Internals(en)), len(Externals(en))
	switch {
	case in > 0 && ex == 0:
		return ModeInternal
	case in == 0:
		if ex == 1 {
			return ModeExternal
		}
		if mirrorActive {
			return ModeMirror
		}
		if ex > 1 {
			return ModeExtend
		}
		return ModeExternal
	default:
		if mirrorActive {
			return ModeMirror
		}
		return ModeExtend
	}
}
