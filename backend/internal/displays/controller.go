package displays

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/equisq/displayswitchDMS/backend/internal/niri"
)

// Niri is the subset of the niri client the controller needs.
type Niri interface {
	Outputs(ctx context.Context) ([]niri.Output, error)
	SetOutput(ctx context.Context, name string, enabled bool) error
}

// Windows is optionally implemented by the niri client; it lets the
// controller give focus back after wl-mirror windows open.
type Windows interface {
	Windows(ctx context.Context) ([]niri.Window, error)
	Workspaces(ctx context.Context) ([]niri.Workspace, error)
	FocusedWindowID(ctx context.Context) (uint64, bool, error)
	FocusWindow(ctx context.Context, id uint64) error
	MoveWindowToMonitor(ctx context.Context, id uint64, output string) error
}

// Mirror is the wl-mirror supervisor.
type Mirror interface {
	Available() bool
	Start(source string, targets []string) error
	Stop()
	Running() bool
	Active() bool
	Matches(source string, targets []string) bool
}

type Options struct {
	InternalPrefixes []string
	// AutoOnConnect is applied when a never-seen external display is plugged
	// in: ModeExtend (default, like GNOME) or "" to keep the current mode.
	AutoOnConnect  Mode
	RestoreOnStart bool
	StatePath      string
	// Settle is how long we wait for niri to report an output as enabled.
	Settle time.Duration
	// MirrorTitlePrefix is the title wl-mirror windows get.
	MirrorTitlePrefix string
}

// Notice is a one-shot message for the UI ("an external display was unplugged").
type Notice struct {
	Kind   string `json:"kind"` // fallback | restored | connected | mirror_failed | error
	Detail string `json:"detail,omitempty"`
}

// Snapshot is everything the UI needs.
type Snapshot struct {
	Seq       uint64                `json:"seq"`
	Displays  []Display             `json:"displays"`
	Requested Mode                  `json:"requested"`
	Effective Mode                  `json:"effective"`
	Fallback  bool                  `json:"fallback"`
	Available map[Mode]Availability `json:"available"`
	Caps      Caps                  `json:"capabilities"`
	Notice    *Notice               `json:"notice,omitempty"`
}

type persisted struct {
	BySet map[string]Mode `json:"bySet"`
}

type Controller struct {
	n    Niri
	m    Mirror
	opt  Options
	caps Caps

	mu        sync.Mutex
	st        persisted
	desired   Mode // what the user wants (may differ from effective on fallback)
	lastSig   string
	lastIdent map[string]bool
	started   bool
	seq       uint64
	applies   []time.Time // reconcile-initiated applies, for the flap guard

	// OnSnapshot is called (without the lock) whenever the visible state changed
	// or a notice has to be delivered.
	OnSnapshot func(Snapshot)
	lastJSON   string
}

func NewController(n Niri, m Mirror, opt Options) *Controller {
	if opt.Settle <= 0 {
		opt.Settle = 2500 * time.Millisecond
	}
	if opt.MirrorTitlePrefix == "" {
		opt.MirrorTitlePrefix = "dms-displayswitch-mirror:"
	}
	c := &Controller{n: n, m: m, opt: opt, st: persisted{BySet: map[string]Mode{}}}
	c.caps = Caps{WlMirror: m != nil && m.Available()}
	c.load()
	return c
}

// ---- persistence ---------------------------------------------------------

func (c *Controller) load() {
	if c.opt.StatePath == "" {
		return
	}
	b, err := os.ReadFile(c.opt.StatePath)
	if err != nil {
		return
	}
	var p persisted
	if json.Unmarshal(b, &p) == nil && p.BySet != nil {
		c.st = p
	}
}

func (c *Controller) save() {
	if c.opt.StatePath == "" {
		return
	}
	b, err := json.MarshalIndent(c.st, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(c.opt.StatePath), 0o755); err != nil {
		return
	}
	tmp := c.opt.StatePath + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, c.opt.StatePath)
	}
}

// ---- reading -------------------------------------------------------------

func (c *Controller) topology(ctx context.Context) ([]Display, error) {
	outs, err := c.n.Outputs(ctx)
	if err != nil {
		return nil, err
	}
	return FromOutputs(outs, c.opt.InternalPrefixes), nil
}

func (c *Controller) snapshotLocked(ds []Display, notice *Notice) Snapshot {
	c.seq++
	eff := Detect(ds, c.m != nil && c.m.Active())
	req := c.desired
	if req == "" {
		req = eff
	}
	return Snapshot{
		Seq:       c.seq,
		Displays:  ds,
		Requested: req,
		Effective: eff,
		Fallback:  req != eff && req != "" && eff != "",
		Available: Availabilities(ds, c.caps),
		Caps:      c.caps,
		Notice:    notice,
	}
}

// Snapshot reads the live state without changing anything.
func (c *Controller) Snapshot(ctx context.Context) (Snapshot, error) {
	ds, err := c.topology(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshotLocked(ds, nil), nil
}

func (c *Controller) emit(s Snapshot) {
	cb := c.OnSnapshot
	if cb == nil {
		return
	}
	cmp := s
	cmp.Seq = 0
	b, _ := json.Marshal(cmp)
	c.mu.Lock()
	same := string(b) == c.lastJSON
	c.lastJSON = string(b)
	c.mu.Unlock()
	if same && s.Notice == nil {
		return
	}
	cb(s)
}

// ---- applying ------------------------------------------------------------

// Apply is the user-initiated path (popup, keybind, CLI).
func (c *Controller) Apply(ctx context.Context, req Mode) (Snapshot, error) {
	if !req.Valid() {
		return Snapshot{}, fmt.Errorf("unknown mode %q", req)
	}
	c.mu.Lock()
	snap, err := c.applyLocked(ctx, req, true)
	c.mu.Unlock()
	if err == nil || snap.Seq != 0 {
		c.emit(snap)
	}
	return snap, err
}

func (c *Controller) applyLocked(ctx context.Context, req Mode, user bool) (Snapshot, error) {
	ds, err := c.topology(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	plan, err := BuildPlan(req, ds, c.caps)
	if err != nil {
		return Snapshot{}, err
	}
	toEnable, toDisable := plan.Diff(ds)

	// 1. Stop mirroring first: its windows must not outlive their output.
	needMirror := plan.Mirror != nil
	if c.m != nil && c.m.Active() && !(needMirror && len(toEnable) == 0 && len(toDisable) == 0 && c.m.Matches(plan.Mirror.Source, plan.Mirror.Targets)) {
		c.m.Stop()
	}

	// 2. Enable before disabling so the desktop is never left without a screen.
	for _, name := range toEnable {
		if err := c.n.SetOutput(ctx, name, true); err != nil {
			return Snapshot{}, fmt.Errorf("enable %s: %w", name, err)
		}
	}
	if len(toEnable) > 0 {
		if !c.waitEnabled(ctx, plan.Enable) {
			// Nothing we asked for came up: do NOT turn the old screens off.
			return Snapshot{}, fmt.Errorf("display %v did not wake up in time, previous layout kept", toEnable)
		}
	}
	var disableErrs []error
	for _, name := range toDisable {
		if err := c.n.SetOutput(ctx, name, false); err != nil {
			disableErrs = append(disableErrs, fmt.Errorf("disable %s: %w", name, err))
		}
	}

	// 3. Mirror.
	var notice *Notice
	if needMirror && !(c.m.Active() && c.m.Running() && c.m.Matches(plan.Mirror.Source, plan.Mirror.Targets)) {
		focused := c.rememberFocus(ctx)
		time.Sleep(250 * time.Millisecond)
		if err := c.m.Start(plan.Mirror.Source, plan.Mirror.Targets); err != nil {
			log.Printf("mirror start failed: %v", err)
			notice = &Notice{Kind: "mirror_failed", Detail: err.Error()}
			plan.Effective = ModeExtend
		} else {
			go c.placeMirrorWindows(plan.Mirror.Targets, focused)
		}
	}

	// 4. Safety net: something must be on.
	ds2, err := c.topology(ctx)
	if err == nil && len(enabled(ds2)) == 0 && len(ds2) > 0 {
		log.Printf("no output enabled after apply, enabling all as a safety net")
		for _, d := range ds2 {
			_ = c.n.SetOutput(ctx, d.Name, true)
		}
		ds2, _ = c.topology(ctx)
		notice = &Notice{Kind: "error", Detail: "safety net: all displays re-enabled"}
	}
	if err != nil {
		ds2 = ds
	}

	c.desired = req
	c.lastSig = Signature(ds2)
	c.lastIdent = identSet(ds2)
	c.started = true
	if user {
		c.st.BySet[c.lastSig] = req
		c.save()
	}
	if plan.Fallback && notice == nil {
		notice = &Notice{Kind: "fallback", Detail: string(plan.Effective)}
	}
	snap := c.snapshotLocked(ds2, notice)
	if len(disableErrs) > 0 {
		return snap, errors.Join(disableErrs...)
	}
	return snap, nil
}

// waitEnabled waits until at least one of the wanted outputs is reported
// enabled by niri.
func (c *Controller) waitEnabled(ctx context.Context, want []string) bool {
	deadline := time.Now().Add(c.opt.Settle)
	for {
		ds, err := c.topology(ctx)
		if err == nil {
			up := map[string]bool{}
			for _, d := range ds {
				up[d.Name] = d.Enabled
			}
			for _, n := range want {
				if up[n] {
					return true
				}
			}
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return false
		}
		time.Sleep(80 * time.Millisecond)
	}
}

// ---- reconciling (hotplug, config reload, crashes) ----------------------

func identSet(ds []Display) map[string]bool {
	m := map[string]bool{}
	for _, d := range ds {
		m[d.Identity] = true
	}
	return m
}

// Reconcile is called whenever the world may have changed. When reassert is
// false it only reacts to a change of the connected display set, so manual
// changes (niri msg output, DMS settings) are left alone.
func (c *Controller) Reconcile(ctx context.Context, reason string, reassert bool) {
	c.mu.Lock()
	snap, notice, changed := c.reconcileLocked(ctx, reason, reassert)
	_ = notice
	c.mu.Unlock()
	if changed {
		c.emit(snap)
	}
}

func (c *Controller) reconcileLocked(ctx context.Context, reason string, reassert bool) (Snapshot, *Notice, bool) {
	ds, err := c.topology(ctx)
	if err != nil {
		return Snapshot{}, nil, false
	}
	if len(ds) == 0 {
		return c.snapshotLocked(ds, nil), nil, true
	}

	sig := Signature(ds)
	sigChanged := !c.started || sig != c.lastSig
	prevIdent := c.lastIdent
	first := !c.started
	c.lastSig, c.lastIdent, c.started = sig, identSet(ds), true

	target := c.desired
	var notice *Notice

	if sigChanged {
		rec, hasRec := c.st.BySet[sig]
		switch {
		case first && !(hasRec && c.opt.RestoreOnStart):
			// Fresh start without a saved layout: adopt whatever niri has.
			c.desired = Detect(ds, false)
			return c.snapshotLocked(ds, nil), nil, true
		case hasRec:
			target = rec
			notice = &Notice{Kind: "restored", Detail: string(rec)}
		case newExternal(prevIdent, ds) && c.opt.AutoOnConnect != "":
			target = c.opt.AutoOnConnect
			notice = &Notice{Kind: "connected", Detail: string(target)}
		}
	} else if !reassert {
		// Same hardware, nobody asked us to enforce anything: only make sure
		// a crashed mirror does not stay half-alive.
		if c.desired == ModeMirror && c.m != nil && c.m.Active() && !c.m.Running() {
			target = ModeExtend
			notice = &Notice{Kind: "mirror_failed"}
			c.m.Stop()
			c.desired = ModeExtend
		} else {
			return c.snapshotLocked(ds, nil), nil, true
		}
	}

	if target == "" {
		return c.snapshotLocked(ds, nil), nil, true
	}

	plan, err := BuildPlan(target, ds, c.caps)
	if err != nil {
		// e.g. mirror without wl-mirror: degrade to extend instead of failing.
		log.Printf("reconcile(%s): plan for %s: %v; using extend", reason, target, err)
		target = ModeExtend
		plan, err = BuildPlan(target, ds, c.caps)
		if err != nil {
			return c.snapshotLocked(ds, nil), nil, true
		}
	}
	toEnable, toDisable := plan.Diff(ds)
	mirrorOK := plan.Mirror == nil && (c.m == nil || !c.m.Active()) ||
		plan.Mirror != nil && c.m != nil && c.m.Running() && c.m.Matches(plan.Mirror.Source, plan.Mirror.Targets)
	if len(toEnable) == 0 && len(toDisable) == 0 && mirrorOK {
		c.desired = target
		if plan.Fallback && notice == nil && sigChanged {
			notice = &Notice{Kind: "fallback", Detail: string(plan.Effective)}
		}
		return c.snapshotLocked(ds, notice), notice, true
	}

	if c.flapping() {
		log.Printf("reconcile(%s): too many automatic changes, pausing", reason)
		return c.snapshotLocked(ds, &Notice{Kind: "error", Detail: "unstable display state, automatic switching paused"}), nil, true
	}

	log.Printf("reconcile(%s): applying %s (enable=%v disable=%v)", reason, target, toEnable, toDisable)
	snap, err := c.applyLocked(ctx, target, false)
	if err != nil && snap.Seq == 0 {
		log.Printf("reconcile apply failed: %v", err)
		return c.snapshotLocked(ds, &Notice{Kind: "error", Detail: err.Error()}), nil, true
	}
	if snap.Notice == nil && notice != nil {
		snap.Notice = notice
	}
	return snap, snap.Notice, true
}

func newExternal(prev map[string]bool, ds []Display) bool {
	if prev == nil {
		return false
	}
	for _, d := range ds {
		if !d.Internal && !prev[d.Identity] {
			return true
		}
	}
	return false
}

// flapping implements a tiny circuit breaker: more than 4 automatic applies
// in 10 seconds means something else is fighting us.
func (c *Controller) flapping() bool {
	now := time.Now()
	keep := c.applies[:0]
	for _, t := range c.applies {
		if now.Sub(t) < 10*time.Second {
			keep = append(keep, t)
		}
	}
	c.applies = keep
	if len(c.applies) >= 4 {
		return true
	}
	c.applies = append(c.applies, now)
	return false
}

// Rescue turns every connected output on, whatever the plan says.
func (c *Controller) Rescue(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m != nil {
		c.m.Stop()
	}
	ds, err := c.topology(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, d := range ds {
		if !d.Enabled {
			errs = append(errs, c.n.SetOutput(ctx, d.Name, true))
		}
	}
	c.desired = ModeExtend
	return errors.Join(errs...)
}

// ---- mirror windows ------------------------------------------------------

func (c *Controller) rememberFocus(ctx context.Context) uint64 {
	w, ok := c.n.(Windows)
	if !ok {
		return 0
	}
	id, has, err := w.FocusedWindowID(ctx)
	if err != nil || !has {
		return 0
	}
	return id
}

// placeMirrorWindows gives focus back to the window the user was in (a new
// full-screen window would otherwise grab the keyboard) and, if niri put a
// mirror window on the wrong output, moves it to its target.
func (c *Controller) placeMirrorWindows(targets []string, prevFocus uint64) {
	w, ok := c.n.(Windows)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	want := map[string]bool{}
	for _, t := range targets {
		want[c.opt.MirrorTitlePrefix+t] = true
	}
	found := map[string]bool{}
	for ctx.Err() == nil {
		wins, err := w.Windows(ctx)
		if err == nil {
			wss, _ := w.Workspaces(ctx)
			wsOut := map[uint64]string{}
			for _, ws := range wss {
				if ws.Output != nil {
					wsOut[ws.ID] = *ws.Output
				}
			}
			for _, win := range wins {
				if win.Title == nil || !want[*win.Title] || found[*win.Title] {
					continue
				}
				found[*win.Title] = true
				target := (*win.Title)[len(c.opt.MirrorTitlePrefix):]
				if win.WorkspaceID != nil {
					if cur, ok := wsOut[*win.WorkspaceID]; ok && cur != target {
						if err := w.MoveWindowToMonitor(ctx, win.ID, target); err != nil {
							log.Printf("mirror: move window to %s: %v", target, err)
						}
					}
				}
			}
		}
		if len(found) == len(want) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if prevFocus != 0 {
		if err := w.FocusWindow(ctx, prevFocus); err != nil {
			log.Printf("mirror: restore focus: %v", err)
		}
	}
}
