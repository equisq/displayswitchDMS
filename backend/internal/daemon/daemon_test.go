package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/equisq/displayswitchDMS/backend/internal/displays"
	"github.com/equisq/displayswitchDMS/backend/internal/niri"
)

// fakeNiriServer speaks niri's real wire protocol on a unix socket.
type fakeNiriServer struct {
	t    *testing.T
	ln   net.Listener
	mu   sync.Mutex
	outs map[string]bool // name -> enabled
	subs []net.Conn
	log  []string
}

func startFakeNiri(t *testing.T, outs map[string]bool) (*fakeNiriServer, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "niri.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeNiriServer{t: t, ln: ln, outs: outs}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		s.mu.Lock()
		for _, c := range s.subs {
			c.Close()
		}
		s.mu.Unlock()
	})
	return s, path
}

func (s *fakeNiriServer) serve(c net.Conn) {
	line, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		c.Close()
		return
	}
	var generic any
	_ = json.Unmarshal(line, &generic)
	switch v := generic.(type) {
	case string:
		switch v {
		case "Outputs":
			s.mu.Lock()
			m := map[string]any{}
			for name, en := range s.outs {
				o := map[string]any{"name": name, "make": "Make", "model": "Model-" + name, "serial": nil,
					"modes": []any{map[string]any{"width": 1920, "height": 1080, "refresh_rate": 60000, "is_preferred": true}}}
				if strings.HasPrefix(name, "HDMI") {
					o["serial"] = "S-" + name
				}
				if en {
					o["current_mode"] = 0
					o["logical"] = map[string]any{"x": 0, "y": 0, "width": 1920, "height": 1080, "scale": 1.0, "transform": "Normal"}
				} else {
					o["current_mode"] = nil
					o["logical"] = nil
				}
				m[name] = o
			}
			s.mu.Unlock()
			writeJSON(c, map[string]any{"Ok": map[string]any{"Outputs": m}})
			c.Close()
		case "EventStream":
			writeJSON(c, map[string]any{"Ok": "Handled"})
			s.mu.Lock()
			s.subs = append(s.subs, c)
			s.mu.Unlock()
		default:
			writeJSON(c, map[string]any{"Err": "unsupported request"})
			c.Close()
		}
	case map[string]any:
		if o, ok := v["Output"].(map[string]any); ok {
			name, _ := o["output"].(string)
			act, _ := o["action"].(string)
			s.mu.Lock()
			if _, exists := s.outs[name]; !exists {
				s.mu.Unlock()
				writeJSON(c, map[string]any{"Err": "no such output"})
				c.Close()
				return
			}
			s.outs[name] = act == "On"
			s.log = append(s.log, strings.ToLower(act)+":"+name)
			s.mu.Unlock()
			writeJSON(c, map[string]any{"Ok": "Handled"})
			s.broadcast(map[string]any{"WorkspacesChanged": map[string]any{"workspaces": []any{}}})
		} else {
			writeJSON(c, map[string]any{"Err": "unsupported"})
		}
		c.Close()
	}
}

func writeJSON(c net.Conn, v any) {
	b, _ := json.Marshal(v)
	_, _ = c.Write(append(b, '\n'))
}

func (s *fakeNiriServer) broadcast(ev any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.subs {
		writeJSON(c, ev)
	}
}

func (s *fakeNiriServer) unplug(name string) {
	s.mu.Lock()
	delete(s.outs, name)
	s.mu.Unlock()
	s.broadcast(map[string]any{"WorkspacesChanged": map[string]any{"workspaces": []any{}}})
}

func (s *fakeNiriServer) plug(name string) {
	s.mu.Lock()
	s.outs[name] = true
	s.mu.Unlock()
	s.broadcast(map[string]any{"WorkspacesChanged": map[string]any{"workspaces": []any{}}})
}

func (s *fakeNiriServer) enabled() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var r []string
	for n, e := range s.outs {
		if e {
			r = append(r, n)
		}
	}
	return r
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func TestDaemonEndToEnd_ExternalOnly_UnplugFallback_Replug(t *testing.T) {
	fake, sock := startFakeNiri(t, map[string]bool{"eDP-1": true, "HDMI-A-1": true})
	t.Setenv("NIRI_SOCKET", sock)
	nc, err := niri.New()
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	out := &syncBuf{}
	ctl := displays.NewController(nc, nil, displays.Options{
		AutoOnConnect: displays.ModeExtend, RestoreOnStart: true,
		StatePath: filepath.Join(dir, "state.json"), Settle: time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Config{
			Controller: ctl, Niri: nc,
			SocketPath: filepath.Join(dir, "d.sock"), LockPath: filepath.Join(dir, "d.lock"),
			Out: out, PollInterval: 200 * time.Millisecond, Debounce: 50 * time.Millisecond,
			ReassertOnReload: true,
		})
	}()
	defer func() { cancel(); <-done }()

	sock2 := filepath.Join(dir, "d.sock")
	waitFor(t, "daemon socket", func() bool { _, err := os.Stat(sock2); return err == nil })
	waitFor(t, "initial state on stdout", func() bool { return strings.Contains(out.String(), `"type":"state"`) })

	// 1. user picks "external only"
	resp, err := Call(sock2, Request{Cmd: "set", Mode: "external"})
	if err != nil || !resp.OK {
		t.Fatalf("set external: %v %+v", err, resp)
	}
	if got := fake.enabled(); len(got) != 1 || got[0] != "HDMI-A-1" {
		t.Fatalf("enabled = %v", got)
	}

	// 2. cable pulled: event + poll -> laptop screen back
	fake.unplug("HDMI-A-1")
	waitFor(t, "fallback to laptop", func() bool { e := fake.enabled(); return len(e) == 1 && e[0] == "eDP-1" })
	waitFor(t, "fallback reported", func() bool {
		return strings.Contains(out.String(), `"kind":"fallback"`)
	})

	// 3. plugged again: external only is restored
	fake.plug("HDMI-A-1")
	waitFor(t, "restore external only", func() bool { e := fake.enabled(); return len(e) == 1 && e[0] == "HDMI-A-1" })

	// every stdout line must be valid JSON
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("bad line %q: %v", l, err)
		}
	}

	// 4. bad requests do not kill the daemon
	if r, _ := Call(sock2, Request{Cmd: "set", Mode: "mirror"}); r.OK || r.Error == "" {
		t.Fatalf("mirror without wl-mirror must be refused: %+v", r)
	}
	if r, _ := Call(sock2, Request{Cmd: "nope"}); r.Error != "bad_request" {
		t.Fatalf("resp = %+v", r)
	}
	if r, err := Call(sock2, Request{Cmd: "ping"}); err != nil || !r.OK {
		t.Fatalf("daemon dead after bad requests: %v %+v", err, r)
	}
}

func TestSecondDaemonIsRejected(t *testing.T) {
	dir := t.TempDir()
	unlock, err := acquireLock(filepath.Join(dir, "l"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	start := time.Now()
	if _, err := acquireLock(filepath.Join(dir, "l"), 300*time.Millisecond); err != ErrAlreadyRunning {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) < 250*time.Millisecond {
		t.Fatal("should have retried before giving up")
	}
}

func TestStdinEOFStopsDaemon(t *testing.T) {
	_, sock := startFakeNiri(t, map[string]bool{"eDP-1": true})
	t.Setenv("NIRI_SOCKET", sock)
	nc, _ := niri.New()
	dir := t.TempDir()
	ctl := displays.NewController(nc, nil, displays.Options{StatePath: filepath.Join(dir, "s.json")})
	pr, pw, _ := os.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), Config{
			Controller: ctl, Niri: nc, SocketPath: filepath.Join(dir, "d.sock"), LockPath: filepath.Join(dir, "d.lock"),
			ExitOnStdinEOF: true, Stdin: pr, PollInterval: time.Second,
		})
	}()
	time.Sleep(1200 * time.Millisecond) // longer than the "closed immediately" guard
	pw.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("daemon did not exit after stdin EOF")
	}
}
