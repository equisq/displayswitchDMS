package mirror

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeBin(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "wl-mirror")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timeout: %s", what)
}

func TestStartRunsOneProcessPerTarget_AndStopKillsThem(t *testing.T) {
	log := filepath.Join(t.TempDir(), "args")
	s := New(fakeBin(t, `echo "$@" >> `+log+`; exec sleep 30`))
	if err := s.Start("eDP-1", []string{"HDMI-A-1", "DP-1"}); err != nil {
		t.Fatal(err)
	}
	if !s.Running() || !s.Matches("eDP-1", []string{"DP-1", "HDMI-A-1"}) {
		t.Fatal("expected a running mirror")
	}
	eventually(t, "both processes logged their args", func() bool {
		b, _ := os.ReadFile(log)
		return strings.Count(string(b), "\n") == 2
	})
	b, _ := os.ReadFile(log)
	for _, want := range []string{"--fullscreen-output HDMI-A-1", "--fullscreen-output DP-1", TitlePrefix + "HDMI-A-1", " eDP-1"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("args missing %q:\n%s", want, b)
		}
	}
	s.Stop()
	if s.Running() || s.Active() {
		t.Fatal("still active after Stop")
	}
}

func TestStartFailsWhenBinaryMissing(t *testing.T) {
	if err := New("").Start("a", []string{"b"}); err == nil {
		t.Fatal("expected error")
	}
	if err := New("/nonexistent/wl-mirror").Start("a", []string{"b"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestCrashLoopIsAbandoned(t *testing.T) {
	s := New(fakeBin(t, `exit 1`))
	failed := make(chan struct{}, 4)
	s.OnFailure = func() { failed <- struct{}{} }
	if err := s.Start("eDP-1", []string{"HDMI-A-1"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-failed:
	case <-time.After(10 * time.Second):
		t.Fatal("crash loop never reported")
	}
	if s.Running() {
		t.Fatal("must not claim to run")
	}
	s.Stop()
}

func TestProcessThatDiesOnceIsRestarted(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "m")
	s := New(fakeBin(t, `if [ ! -e `+marker+` ]; then touch `+marker+`; sleep 0.2; exit 1; fi; exec sleep 30`))
	if err := s.Start("eDP-1", []string{"HDMI-A-1"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	eventually(t, "restarted after a crash", s.Running)
	s.Stop()
}

func TestRestartReplacesPreviousSession(t *testing.T) {
	s := New(fakeBin(t, `exec sleep 30`))
	_ = s.Start("eDP-1", []string{"HDMI-A-1"})
	_ = s.Start("eDP-1", []string{"DP-1"})
	if !s.Matches("eDP-1", []string{"DP-1"}) || s.Matches("eDP-1", []string{"HDMI-A-1"}) {
		t.Fatal("second Start must replace the first")
	}
	s.Stop()
}
