package displays

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/equisq/displayswitchDMS/backend/internal/niri"
)

type fakeOut struct {
	name, make, model, serial string
	enabled                   bool
}

// fakeNiri is an in-memory niri.
type fakeNiri struct {
	mu    sync.Mutex
	outs  map[string]*fakeOut
	calls []string // "on:NAME" / "off:NAME"

	failOn  map[string]error // SetOutput(name, true) errors
	noWake  map[string]bool  // On succeeds but output never comes up
	failOff map[string]error
	outsErr error
}

func newFakeNiri(outs ...fakeOut) *fakeNiri {
	f := &fakeNiri{outs: map[string]*fakeOut{}, failOn: map[string]error{}, noWake: map[string]bool{}, failOff: map[string]error{}}
	for i := range outs {
		o := outs[i]
		f.outs[o.name] = &o
	}
	return f
}

func laptop() fakeOut { return fakeOut{"eDP-1", "BOE", "0x0a1b", "", true} }
func dell(port string) fakeOut {
	return fakeOut{port, "Dell Inc.", "U2720Q", "ABC123", true}
}
func lg(port string) fakeOut { return fakeOut{port, "LG", "27UL850", "XYZ789", true} }

func (f *fakeNiri) Outputs(ctx context.Context) ([]niri.Output, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.outsErr != nil {
		return nil, f.outsErr
	}
	var res []niri.Output
	for _, o := range f.outs {
		no := niri.Output{Name: o.name, Make: o.make, Model: o.model,
			Modes: []niri.Mode{{Width: 1920, Height: 1080, RefreshRate: 60000, IsPreferred: true}}}
		if o.serial != "" {
			s := o.serial
			no.Serial = &s
		}
		if o.enabled {
			z := 0
			no.CurrentMode = &z
			no.Logical = &niri.Logical{Width: 1920, Height: 1080, Scale: 1}
		}
		res = append(res, no)
	}
	sort.Slice(res, func(i, j int) bool { return res[i].Name < res[j].Name })
	return res, nil
}

func (f *fakeNiri) SetOutput(ctx context.Context, name string, on bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.outs[name]
	if !ok {
		return fmt.Errorf("no such output %s", name)
	}
	if on {
		f.calls = append(f.calls, "on:"+name)
		if err := f.failOn[name]; err != nil {
			return err
		}
		if !f.noWake[name] {
			o.enabled = true
		}
		return nil
	}
	f.calls = append(f.calls, "off:"+name)
	if err := f.failOff[name]; err != nil {
		return err
	}
	o.enabled = false
	return nil
}

func (f *fakeNiri) unplug(name string) {
	f.mu.Lock()
	delete(f.outs, name)
	f.mu.Unlock()
}

// plug adds an output the way niri does: enabled by default.
func (f *fakeNiri) plug(o fakeOut) {
	f.mu.Lock()
	o.enabled = true
	f.outs[o.name] = &o
	f.mu.Unlock()
}

// configReload simulates niri forgetting temporary changes.
func (f *fakeNiri) configReload() {
	f.mu.Lock()
	for _, o := range f.outs {
		o.enabled = true
	}
	f.mu.Unlock()
}

func (f *fakeNiri) enabledNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n []string
	for _, o := range f.outs {
		if o.enabled {
			n = append(n, o.name)
		}
	}
	sort.Strings(n)
	return n
}

func (f *fakeNiri) resetCalls() { f.mu.Lock(); f.calls = nil; f.mu.Unlock() }
func (f *fakeNiri) callList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// fakeMirror records what wl-mirror would do.
type fakeMirror struct {
	avail   bool
	source  string
	targets []string
	running bool
	starts  int
	stops   int
	failErr error
}

func (m *fakeMirror) Available() bool { return m.avail }
func (m *fakeMirror) Start(s string, t []string) error {
	if m.failErr != nil {
		return m.failErr
	}
	m.source, m.targets, m.running = s, t, true
	m.starts++
	return nil
}
func (m *fakeMirror) Stop()         { m.source, m.targets, m.running = "", nil, false; m.stops++ }
func (m *fakeMirror) Running() bool { return m.running && len(m.targets) > 0 }
func (m *fakeMirror) Active() bool  { return len(m.targets) > 0 }
func (m *fakeMirror) Matches(s string, t []string) bool {
	if m.source != s || len(m.targets) != len(t) {
		return false
	}
	for i := range t {
		if m.targets[i] != t[i] {
			return false
		}
	}
	return true
}

var errBoom = errors.New("boom")
