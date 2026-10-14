package displays

import (
	"errors"
	"reflect"
	"sort"
	"testing"
)

func d(name string, internal, en bool) Display {
	return Display{Name: name, Internal: internal, Enabled: en, Identity: name}
}

func sorted(s []string) []string { c := append([]string(nil), s...); sort.Strings(c); return c }

func TestBuildPlan(t *testing.T) {
	lap := d("eDP-1", true, true)
	h1 := d("HDMI-A-1", false, true)
	dp := d("DP-1", false, true)
	cases := []struct {
		name     string
		req      Mode
		ds       []Display
		caps     Caps
		eff      Mode
		fallback bool
		enable   []string
		disable  []string
		mirror   *MirrorPlan
		err      error
	}{
		{"internal ok", ModeInternal, []Display{lap, h1}, Caps{}, ModeInternal, false, []string{"eDP-1"}, []string{"HDMI-A-1"}, nil, nil},
		{"internal on desktop", ModeInternal, []Display{h1}, Caps{}, "", false, nil, nil, nil, ErrNoInternal},
		{"external ok", ModeExternal, []Display{lap, h1, dp}, Caps{}, ModeExternal, false, []string{"DP-1", "HDMI-A-1"}, []string{"eDP-1"}, nil, nil},
		{"external falls back", ModeExternal, []Display{lap}, Caps{}, ModeInternal, true, []string{"eDP-1"}, nil, nil, nil},
		{"external on one desktop monitor", ModeExternal, []Display{h1}, Caps{}, ModeExternal, false, []string{"HDMI-A-1"}, nil, nil, nil},
		{"extend ok", ModeExtend, []Display{lap, h1}, Caps{}, ModeExtend, false, []string{"HDMI-A-1", "eDP-1"}, nil, nil, nil},
		{"extend single laptop", ModeExtend, []Display{lap}, Caps{}, ModeInternal, true, []string{"eDP-1"}, nil, nil, nil},
		{"mirror ok", ModeMirror, []Display{lap, h1, dp}, Caps{WlMirror: true}, ModeMirror, false,
			[]string{"DP-1", "HDMI-A-1", "eDP-1"}, nil, &MirrorPlan{Source: "eDP-1", Targets: []string{"HDMI-A-1", "DP-1"}}, nil},
		{"mirror needs wl-mirror", ModeMirror, []Display{lap, h1}, Caps{}, "", false, nil, nil, nil, ErrNoWlMirror},
		{"mirror single falls back", ModeMirror, []Display{lap}, Caps{WlMirror: true}, ModeInternal, true, []string{"eDP-1"}, nil, nil, nil},
		{"no displays", ModeExtend, nil, Caps{}, "", false, nil, nil, nil, ErrNoDisplays},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := BuildPlan(c.req, c.ds, c.caps)
			if !errors.Is(err, c.err) {
				t.Fatalf("err = %v, want %v", err, c.err)
			}
			if err != nil {
				return
			}
			if p.Effective != c.eff || p.Fallback != c.fallback {
				t.Errorf("effective=%s fallback=%v, want %s %v", p.Effective, p.Fallback, c.eff, c.fallback)
			}
			if len(p.Enable) == 0 {
				t.Errorf("INVARIANT: plan enables nothing")
			}
			if !reflect.DeepEqual(sorted(p.Enable), sorted(c.enable)) {
				t.Errorf("enable = %v, want %v", p.Enable, c.enable)
			}
			if len(c.disable) > 0 && !reflect.DeepEqual(sorted(p.Disable), sorted(c.disable)) {
				t.Errorf("disable = %v, want %v", p.Disable, c.disable)
			}
			if !reflect.DeepEqual(p.Mirror, c.mirror) {
				t.Errorf("mirror = %+v, want %+v", p.Mirror, c.mirror)
			}
		})
	}
}

func TestMirrorSourceOnDesktopIsLeftmost(t *testing.T) {
	a := Display{Name: "DP-2", Enabled: true, X: 1920}
	b := Display{Name: "DP-1", Enabled: true, X: 0}
	p, err := BuildPlan(ModeMirror, []Display{a, b}, Caps{WlMirror: true})
	if err != nil || p.Mirror.Source != "DP-1" {
		t.Fatalf("source = %+v err=%v", p.Mirror, err)
	}
}

func TestAvailabilities(t *testing.T) {
	a := Availabilities([]Display{d("eDP-1", true, true)}, Caps{WlMirror: true})
	if !a[ModeInternal].Available || a[ModeExternal].Available || a[ModeExtend].Available || a[ModeMirror].Available {
		t.Fatalf("laptop only: %+v", a)
	}
	if a[ModeExternal].Reason != "no_external" || a[ModeMirror].Reason != "need_two" {
		t.Fatalf("reasons: %+v", a)
	}
	a = Availabilities([]Display{d("eDP-1", true, true), d("DP-1", false, true)}, Caps{})
	if a[ModeMirror].Available || a[ModeMirror].Reason != "no_wl_mirror" || !a[ModeExtend].Available {
		t.Fatalf("no wl-mirror: %+v", a)
	}
}

func TestDetect(t *testing.T) {
	lap, ext := d("eDP-1", true, true), d("DP-1", false, true)
	offLap := d("eDP-1", true, false)
	cases := []struct {
		ds     []Display
		mirror bool
		want   Mode
	}{
		{[]Display{lap}, false, ModeInternal},
		{[]Display{lap, d("DP-1", false, false)}, false, ModeInternal},
		{[]Display{offLap, ext}, false, ModeExternal},
		{[]Display{lap, ext}, false, ModeExtend},
		{[]Display{lap, ext}, true, ModeMirror},
		{[]Display{offLap, d("DP-1", false, false)}, false, ""},
		{[]Display{ext, d("DP-2", false, true)}, false, ModeExtend},
	}
	for i, c := range cases {
		if got := Detect(c.ds, c.mirror); got != c.want {
			t.Errorf("case %d: got %q want %q", i, got, c.want)
		}
	}
}

func TestInternalNames(t *testing.T) {
	for n, want := range map[string]bool{"eDP-1": true, "edp-2": true, "LVDS-1": true, "DSI-1": true, "HDMI-A-1": false, "DP-3": false} {
		if IsInternalName(n, nil) != want {
			t.Errorf("%s: want %v", n, want)
		}
	}
	if !IsInternalName("Virtual-1", []string{"virtual"}) {
		t.Error("custom prefix ignored")
	}
}

func TestSignatureIgnoresPortAndEnabledState(t *testing.T) {
	f1 := newFakeNiri(laptop(), dell("HDMI-A-1"))
	f2 := newFakeNiri(laptop(), dell("DP-3"))
	f2.outs["eDP-1"].enabled = false
	o1, _ := f1.Outputs(nil)
	o2, _ := f2.Outputs(nil)
	if Signature(FromOutputs(o1, nil)) != Signature(FromOutputs(o2, nil)) {
		t.Fatal("signature must not depend on port or enabled state")
	}
	f3 := newFakeNiri(laptop(), lg("DP-3"))
	o3, _ := f3.Outputs(nil)
	if Signature(FromOutputs(o1, nil)) == Signature(FromOutputs(o3, nil)) {
		t.Fatal("different monitors must have different signatures")
	}
}

func TestIdenticalMonitorsGetDistinctIdentities(t *testing.T) {
	a, b := dell("DP-1"), dell("DP-2")
	f := newFakeNiri(laptop(), a, b)
	o, _ := f.Outputs(nil)
	ds := FromOutputs(o, nil)
	ids := map[string]bool{}
	for _, x := range ds {
		ids[x.Identity] = true
	}
	if len(ids) != 3 {
		t.Fatalf("identities collide: %v", ids)
	}
}
