package displays

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func newTestController(t *testing.T, f *fakeNiri, m *fakeMirror) *Controller {
	t.Helper()
	opt := Options{
		AutoOnConnect:  ModeExtend,
		RestoreOnStart: true,
		StatePath:      filepath.Join(t.TempDir(), "state.json"),
		Settle:         150 * time.Millisecond,
	}
	if m == nil {
		m = &fakeMirror{}
	}
	return NewController(f, m, opt)
}

func idx(calls []string, s string) int {
	for i, c := range calls {
		if c == s {
			return i
		}
	}
	return -1
}

func TestExternalOnly_EnablesBeforeDisablingInternal(t *testing.T) {
	f := newFakeNiri(laptop(), dell("HDMI-A-1"))
	f.outs["HDMI-A-1"].enabled = false // plugged but off
	c := newTestController(t, f, nil)

	snap, err := c.Apply(context.Background(), ModeExternal)
	if err != nil {
		t.Fatal(err)
	}
	calls := f.callList()
	on, off := idx(calls, "on:HDMI-A-1"), idx(calls, "off:eDP-1")
	if on < 0 || off < 0 || on > off {
		t.Fatalf("must enable the external before disabling the laptop: %v", calls)
	}
	if !reflect.DeepEqual(f.enabledNames(), []string{"HDMI-A-1"}) {
		t.Fatalf("enabled = %v", f.enabledNames())
	}
	if snap.Effective != ModeExternal {
		t.Fatalf("effective = %s", snap.Effective)
	}
}

func TestExternalOnly_MonitorDoesNotWakeUp_KeepsLaptopOn(t *testing.T) {
	f := newFakeNiri(laptop(), dell("HDMI-A-1"))
	f.outs["HDMI-A-1"].enabled = false
	f.noWake["HDMI-A-1"] = true // On is accepted but the monitor never comes up
	c := newTestController(t, f, nil)

	_, err := c.Apply(context.Background(), ModeExternal)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !reflect.DeepEqual(f.enabledNames(), []string{"eDP-1"}) {
		t.Fatalf("laptop screen must stay on, enabled = %v", f.enabledNames())
	}
	if idx(f.callList(), "off:eDP-1") >= 0 {
		t.Fatal("laptop screen was disabled")
	}
}

func TestExternalOnly_EnableRejected_NothingDisabled(t *testing.T) {
	f := newFakeNiri(laptop(), dell("HDMI-A-1"))
	f.outs["HDMI-A-1"].enabled = false
	f.failOn["HDMI-A-1"] = errBoom
	c := newTestController(t, f, nil)
	if _, err := c.Apply(context.Background(), ModeExternal); err == nil {
		t.Fatal("expected error")
	}
	if !reflect.DeepEqual(f.enabledNames(), []string{"eDP-1"}) {
		t.Fatalf("enabled = %v", f.enabledNames())
	}
}

func TestUnplugInExternalMode_FallsBackToLaptop_ReplugRestores(t *testing.T) {
	ctx := context.Background()
	f := newFakeNiri(laptop(), dell("HDMI-A-1"))
	c := newTestController(t, f, nil)
	c.Reconcile(ctx, "start", false)
	if _, err := c.Apply(ctx, ModeExternal); err != nil {
		t.Fatal(err)
	}

	// Cable pulled: niri removes the output, the laptop panel is still Off.
	f.unplug("HDMI-A-1")
	c.Reconcile(ctx, "unplug", false)
	if !reflect.DeepEqual(f.enabledNames(), []string{"eDP-1"}) {
		t.Fatalf("after unplug enabled = %v, want the laptop screen", f.enabledNames())
	}
	snap, _ := c.Snapshot(ctx)
	if snap.Effective != ModeInternal || snap.Requested != ModeExternal || !snap.Fallback {
		t.Fatalf("snapshot: %+v", snap)
	}

	// Plugged again, on another port: back to external only.
	f.plug(dell("DP-2"))
	c.Reconcile(ctx, "replug", false)
	if !reflect.DeepEqual(f.enabledNames(), []string{"DP-2"}) {
		t.Fatalf("after replug enabled = %v, want only DP-2", f.enabledNames())
	}
}

func TestUnplugInExtendMode_StaysUsable(t *testing.T) {
	ctx := context.Background()
	f := newFakeNiri(laptop(), dell("HDMI-A-1"))
	c := newTestController(t, f, nil)
	c.Reconcile(ctx, "start", false)
	if _, err := c.Apply(ctx, ModeExtend); err != nil {
		t.Fatal(err)
	}
	f.unplug("HDMI-A-1")
	c.Reconcile(ctx, "unplug", false)
	snap, _ := c.Snapshot(ctx)
	if snap.Effective != ModeInternal || len(f.enabledNames()) != 1 {
		t.Fatalf("snapshot %+v enabled %v", snap, f.enabledNames())
	}
}

func TestUnplugLastExternalWhileLaptopOffAndNoLaptopRecord(t *testing.T) {
	// Worst case: laptop panel off (external mode), then the monitor
	// disappears without any saved record for the new topology.
	ctx := context.Background()
	f := newFakeNiri(laptop(), dell("HDMI-A-1"))
	c := newTestController(t, f, nil)
	c.Reconcile(ctx, "start", false)
	_, _ = c.Apply(ctx, ModeExternal)
	delete(c.st.BySet, Signature(mustDisplays(t, f))) // pretend nothing was saved
	f.unplug("HDMI-A-1")
	c.Reconcile(ctx, "unplug", false)
	if len(f.enabledNames()) != 1 || f.enabledNames()[0] != "eDP-1" {
		t.Fatalf("machine would be dark: %v", f.enabledNames())
	}
}

func mustDisplays(t *testing.T, f *fakeNiri) []Display {
	t.Helper()
	o, err := f.Outputs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return FromOutputs(o, nil)
}

func TestNewMonitorPluggedInLaptopOnlyMode_AutoExtends(t *testing.T) {
	ctx := context.Background()
	f := newFakeNiri(laptop())
	c := newTestController(t, f, nil)
	c.Reconcile(ctx, "start", false)
	if _, err := c.Apply(ctx, ModeInternal); err != nil {
		t.Fatal(err)
	}
	f.plug(lg("HDMI-A-1")) // niri brings it up enabled
	c.Reconcile(ctx, "plug", false)
	if !reflect.DeepEqual(f.enabledNames(), []string{"HDMI-A-1", "eDP-1"}) {
		t.Fatalf("enabled = %v", f.enabledNames())
	}
	snap, _ := c.Snapshot(ctx)
	if snap.Effective != ModeExtend {
		t.Fatalf("effective = %s", snap.Effective)
	}
}

func TestKnownMonitorPluggedInLaptopOnlyMode_StaysOff(t *testing.T) {
	ctx := context.Background()
	f := newFakeNiri(laptop(), lg("HDMI-A-1"))
	c := newTestController(t, f, nil)
	c.Reconcile(ctx, "start", false)
	if _, err := c.Apply(ctx, ModeInternal); err != nil { // user picked "PC only" with this monitor attached
		t.Fatal(err)
	}
	f.unplug("HDMI-A-1")
	c.Reconcile(ctx, "unplug", false)
	f.plug(lg("HDMI-A-1"))
	c.Reconcile(ctx, "replug", false)
	if !reflect.DeepEqual(f.enabledNames(), []string{"eDP-1"}) {
		t.Fatalf("saved choice for this monitor set must win, enabled = %v", f.enabledNames())
	}
}

func TestAutoOnConnectKeep_RespectsCurrentMode(t *testing.T) {
	ctx := context.Background()
	f := newFakeNiri(laptop())
	c := newTestController(t, f, nil)
	c.opt.AutoOnConnect = ""
	c.Reconcile(ctx, "start", false)
	_, _ = c.Apply(ctx, ModeInternal)
	f.plug(lg("HDMI-A-1"))
	c.Reconcile(ctx, "plug", false)
	// desired is internal-only: the new monitor is switched off again.
	if !reflect.DeepEqual(f.enabledNames(), []string{"eDP-1"}) {
		t.Fatalf("enabled = %v", f.enabledNames())
	}
}

func TestMirror_StartsWlMirrorOnTargets_AndStopsOnUnplug(t *testing.T) {
	ctx := context.Background()
	f := newFakeNiri(laptop(), dell("HDMI-A-1"), lg("DP-1"))
	m := &fakeMirror{avail: true}
	c := newTestController(t, f, m)
	c.Reconcile(ctx, "start", false)

	snap, err := c.Apply(ctx, ModeMirror)
	if err != nil {
		t.Fatal(err)
	}
	if m.source != "eDP-1" || !reflect.DeepEqual(sorted(m.targets), []string{"DP-1", "HDMI-A-1"}) {
		t.Fatalf("mirror = %s -> %v", m.source, m.targets)
	}
	if snap.Effective != ModeMirror {
		t.Fatalf("effective = %s", snap.Effective)
	}

	f.unplug("HDMI-A-1")
	c.Reconcile(ctx, "unplug", false)
	if m.source != "eDP-1" || !reflect.DeepEqual(m.targets, []string{"DP-1"}) {
		t.Fatalf("mirror should now only feed DP-1: %s -> %v", m.source, m.targets)
	}

	f.unplug("DP-1")
	c.Reconcile(ctx, "unplug2", false)
	if m.Active() {
		t.Fatalf("mirror must stop when nothing is left to mirror onto: %v", m.targets)
	}
	snap, _ = c.Snapshot(ctx)
	if snap.Effective != ModeInternal {
		t.Fatalf("effective = %s", snap.Effective)
	}
}

func TestMirror_StoppedBeforeLeavingMirrorMode(t *testing.T) {
	ctx := context.Background()
	f := newFakeNiri(laptop(), dell("HDMI-A-1"))
	m := &fakeMirror{avail: true}
	c := newTestController(t, f, m)
	c.Reconcile(ctx, "start", false)
	_, _ = c.Apply(ctx, ModeMirror)
	_, err := c.Apply(ctx, ModeInternal)
	if err != nil {
		t.Fatal(err)
	}
	if m.Active() {
		t.Fatal("wl-mirror still running after switching to PC only")
	}
}

func TestMirror_WithoutWlMirrorIsRefused(t *testing.T) {
	f := newFakeNiri(laptop(), dell("HDMI-A-1"))
	c := newTestController(t, f, &fakeMirror{avail: false})
	if _, err := c.Apply(context.Background(), ModeMirror); err == nil {
		t.Fatal("expected ErrNoWlMirror")
	}
	if len(f.callList()) != 0 {
		t.Fatalf("nothing must be touched: %v", f.callList())
	}
}

func TestMirror_StartFailureDegradesToExtend(t *testing.T) {
	f := newFakeNiri(laptop(), dell("HDMI-A-1"))
	m := &fakeMirror{avail: true, failErr: errBoom}
	c := newTestController(t, f, m)
	snap, err := c.Apply(context.Background(), ModeMirror)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Notice == nil || snap.Notice.Kind != "mirror_failed" || snap.Effective != ModeExtend {
		t.Fatalf("snap = %+v notice=%+v", snap, snap.Notice)
	}
}

func TestMirrorProcessDies_DegradesToExtend(t *testing.T) {
	ctx := context.Background()
	f := newFakeNiri(laptop(), dell("HDMI-A-1"))
	m := &fakeMirror{avail: true}
	c := newTestController(t, f, m)
	c.Reconcile(ctx, "start", false)
	_, _ = c.Apply(ctx, ModeMirror)
	m.running = false // wl-mirror crashed for good
	c.Reconcile(ctx, "mirror", false)
	snap, _ := c.Snapshot(ctx)
	if snap.Effective != ModeExtend {
		t.Fatalf("effective = %s", snap.Effective)
	}
}

func TestConfigReload_ReassertsLayoutOnlyWhenAsked(t *testing.T) {
	ctx := context.Background()
	f := newFakeNiri(laptop(), dell("HDMI-A-1"))
	c := newTestController(t, f, nil)
	c.Reconcile(ctx, "start", false)
	_, _ = c.Apply(ctx, ModeExternal)

	f.configReload() // niri forgets the temporary "off" of the laptop panel
	c.Reconcile(ctx, "poll", false)
	if len(f.enabledNames()) != 2 {
		t.Fatalf("manual/unknown changes must not be fought: %v", f.enabledNames())
	}
	c.Reconcile(ctx, "config", true)
	if !reflect.DeepEqual(f.enabledNames(), []string{"HDMI-A-1"}) {
		t.Fatalf("enabled = %v", f.enabledNames())
	}
}

func TestStartup_NoSavedLayout_AdoptsCurrentState(t *testing.T) {
	ctx := context.Background()
	f := newFakeNiri(laptop(), dell("HDMI-A-1"))
	c := newTestController(t, f, nil)
	c.Reconcile(ctx, "start", false)
	if len(f.callList()) != 0 {
		t.Fatalf("first run must not touch outputs: %v", f.callList())
	}
	snap, _ := c.Snapshot(ctx)
	if snap.Requested != ModeExtend {
		t.Fatalf("requested = %s", snap.Requested)
	}
}

func TestStartup_RestoresSavedLayoutAcrossRestarts(t *testing.T) {
	ctx := context.Background()
	f := newFakeNiri(laptop(), dell("HDMI-A-1"))
	c1 := newTestController(t, f, nil)
	c1.Reconcile(ctx, "start", false)
	_, _ = c1.Apply(ctx, ModeExternal)

	// New login: niri brings everything up again, a fresh daemon starts.
	f.configReload()
	c2 := NewController(f, &fakeMirror{}, c1.opt)
	c2.Reconcile(ctx, "start", false)
	if !reflect.DeepEqual(f.enabledNames(), []string{"HDMI-A-1"}) {
		t.Fatalf("enabled = %v", f.enabledNames())
	}
}

func TestRescue_ReenablesEverything(t *testing.T) {
	ctx := context.Background()
	f := newFakeNiri(laptop(), dell("HDMI-A-1"))
	c := newTestController(t, f, nil)
	// A broken niri that reports "On" as success but disables the output right
	// away would black the screen out in external mode; the post-check must
	// notice an empty enabled set and re-enable everything.
	if _, err := c.Apply(ctx, ModeExternal); err != nil {
		t.Fatal(err)
	}
	f.outs["HDMI-A-1"].enabled = false
	f.outs["eDP-1"].enabled = false
	if err := c.Rescue(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.enabledNames()) != 2 {
		t.Fatalf("enabled = %v", f.enabledNames())
	}
}

func TestRepeatedFailuresDoNotLoopForever(t *testing.T) {
	ctx := context.Background()
	f := newFakeNiri(laptop(), dell("HDMI-A-1"))
	c := newTestController(t, f, nil)
	c.Reconcile(ctx, "start", false)
	_, _ = c.Apply(ctx, ModeExternal)
	for i := 0; i < 12; i++ {
		f.configReload() // something keeps re-enabling the laptop
		c.Reconcile(ctx, "config", true)
	}
	n := 0
	for _, call := range f.callList() {
		if call == "off:eDP-1" {
			n++
		}
	}
	if n > 6 {
		t.Fatalf("circuit breaker did not trip: %d disables", n)
	}
}

func TestNiriUnreachable_IsReportedNotFatal(t *testing.T) {
	f := newFakeNiri(laptop())
	f.outsErr = errBoom
	c := newTestController(t, f, nil)
	if _, err := c.Apply(context.Background(), ModeInternal); err == nil {
		t.Fatal("expected error")
	}
	c.Reconcile(context.Background(), "poll", false) // must not panic
}

func TestSnapshotCallbackDedupes(t *testing.T) {
	ctx := context.Background()
	f := newFakeNiri(laptop(), dell("HDMI-A-1"))
	c := newTestController(t, f, nil)
	n := 0
	c.OnSnapshot = func(Snapshot) { n++ }
	c.Reconcile(ctx, "a", false)
	c.Reconcile(ctx, "b", false)
	c.Reconcile(ctx, "c", false)
	if n != 1 {
		t.Fatalf("callbacks = %d, want 1", n)
	}
}
