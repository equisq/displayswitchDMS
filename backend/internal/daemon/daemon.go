// Package daemon glues the controller to the outside world: niri's event
// stream and a poll loop (hotplug), a unix socket for commands, and a
// newline-delimited JSON state stream on stdout for the DMS plugin.
package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/equisq/displayswitchDMS/backend/internal/displays"
	"github.com/equisq/displayswitchDMS/backend/internal/niri"
)

type Config struct {
	Controller       *displays.Controller
	Niri             *niri.Client
	SocketPath       string
	LockPath         string
	Out              io.Writer
	ExitOnStdinEOF   bool
	Stdin            io.Reader
	PollInterval     time.Duration
	Debounce         time.Duration
	ReassertOnReload bool
	Stop             func() // called on shutdown (stops wl-mirror)
}

type Request struct {
	Cmd  string `json:"cmd"` // status | set | rescue
	Mode string `json:"mode,omitempty"`
}

type Response struct {
	OK      bool               `json:"ok"`
	Error   string             `json:"error,omitempty"` // machine readable code
	Message string             `json:"message,omitempty"`
	State   *displays.Snapshot `json:"state,omitempty"`
}

// ErrAlreadyRunning is returned when another daemon holds the lock.
var ErrAlreadyRunning = errors.New("another displayswitch-dmsd daemon is already running")

type trigger struct {
	reason   string
	reassert bool
}

type server struct {
	cfg  Config
	outM sync.Mutex
	trig chan trigger
}

// Run blocks until ctx is cancelled (or stdin closes when requested).
func Run(ctx context.Context, cfg Config) error {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 2 * time.Second
	}
	if cfg.Debounce <= 0 {
		cfg.Debounce = 350 * time.Millisecond
	}
	if cfg.Out == nil {
		cfg.Out = io.Discard
	}

	unlock, err := acquireLock(cfg.LockPath, 3*time.Second)
	if err != nil {
		return err
	}
	defer unlock()

	_ = os.Remove(cfg.SocketPath)
	ln, err := net.Listen("unix", cfg.SocketPath)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.SocketPath, err)
	}
	_ = os.Chmod(cfg.SocketPath, 0o600)
	defer os.Remove(cfg.SocketPath)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	s := &server{cfg: cfg, trig: make(chan trigger, 32)}
	cfg.Controller.OnSnapshot = s.publish

	go func() { <-ctx.Done(); ln.Close() }()
	go s.acceptLoop(ctx, ln)
	go s.reconcileLoop(ctx)
	go s.watchEvents(ctx)
	go s.poll(ctx)
	if cfg.ExitOnStdinEOF {
		go s.watchStdin(cancel)
	}

	s.fire("start", false)
	<-ctx.Done()
	if cfg.Stop != nil {
		cfg.Stop()
	}
	return nil
}

func acquireLock(path string, wait time.Duration) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, ErrAlreadyRunning
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// ---- state stream --------------------------------------------------------

func (s *server) publish(snap displays.Snapshot) {
	ev := struct {
		Type string `json:"type"`
		displays.Snapshot
	}{"state", snap}
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	s.outM.Lock()
	defer s.outM.Unlock()
	_, _ = s.cfg.Out.Write(append(b, '\n'))
}

// ---- triggers ------------------------------------------------------------

func (s *server) fire(reason string, reassert bool) {
	select {
	case s.trig <- trigger{reason, reassert}:
	default: // a reconcile is already pending, it will see the latest state
	}
}

// reconcileLoop debounces bursts of events (plugging a monitor produces many).
func (s *server) reconcileLoop(ctx context.Context) {
	for {
		var t trigger
		select {
		case <-ctx.Done():
			return
		case t = <-s.trig:
		}
		timer := time.NewTimer(s.cfg.Debounce)
	drain:
		for {
			select {
			case n := <-s.trig:
				t.reassert = t.reassert || n.reassert
				t.reason = n.reason
			case <-timer.C:
				break drain
			case <-ctx.Done():
				timer.Stop()
				return
			}
		}
		rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		s.cfg.Controller.Reconcile(rctx, t.reason, t.reassert)
		cancel()
	}
}

func (s *server) poll(ctx context.Context) {
	tk := time.NewTicker(s.cfg.PollInterval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			s.fire("poll", false)
		}
	}
}

// watchEvents follows niri's event stream. niri (as of 26.4) has no dedicated
// output hotplug event, but WorkspacesChanged fires whenever an output appears
// or vanishes; newer versions may add OutputsChanged. The poll loop covers
// anything missed.
func (s *server) watchEvents(ctx context.Context) {
	for ctx.Err() == nil {
		ch, err := s.cfg.Niri.Events(ctx)
		if err != nil {
			log.Printf("niri event stream: %v (retrying)", err)
			sleep(ctx, 2*time.Second)
			continue
		}
		for ev := range ch {
			switch ev.Kind {
			case "WorkspacesChanged", "OutputsChanged":
				s.fire("event:"+ev.Kind, false)
			case "ConfigLoaded":
				s.fire("event:ConfigLoaded", s.cfg.ReassertOnReload)
			}
		}
		if ctx.Err() == nil {
			log.Printf("niri event stream closed, reconnecting")
			sleep(ctx, time.Second)
		}
	}
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// watchStdin exits when the parent (the Quickshell process) goes away, so we
// never outlive the shell and never leave wl-mirror windows behind.
func (s *server) watchStdin(cancel context.CancelFunc) {
	in := s.cfg.Stdin
	if in == nil {
		in = os.Stdin
	}
	began := time.Now()
	_, _ = io.Copy(io.Discard, in)
	if time.Since(began) < time.Second {
		log.Printf("stdin closed immediately; not using it as a parent-death signal")
		return
	}
	log.Printf("stdin closed: parent is gone, shutting down")
	cancel()
}

// ---- command socket -------------------------------------------------------

func (s *server) acceptLoop(ctx context.Context, ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go s.handle(ctx, c)
	}
}

func (s *server) handle(ctx context.Context, c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(30 * time.Second))
	line, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return
	}
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		reply(c, Response{Error: "bad_request", Message: err.Error()})
		return
	}
	reply(c, s.dispatch(ctx, req))
}

func reply(c net.Conn, r Response) {
	b, _ := json.Marshal(r)
	_, _ = c.Write(append(b, '\n'))
}

func (s *server) dispatch(ctx context.Context, req Request) Response {
	ctl := s.cfg.Controller
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	switch req.Cmd {
	case "ping":
		return Response{OK: true}
	case "status":
		snap, err := ctl.Snapshot(cctx)
		if err != nil {
			return Response{Error: "niri", Message: err.Error()}
		}
		return Response{OK: true, State: &snap}
	case "set":
		snap, err := ctl.Apply(cctx, displays.Mode(req.Mode))
		if err != nil && snap.Seq == 0 {
			return Response{Error: errorCode(err), Message: err.Error()}
		}
		r := Response{OK: true, State: &snap}
		if err != nil { // applied, with non fatal problems
			r.Message = err.Error()
		}
		return r
	case "rescue":
		if err := ctl.Rescue(cctx); err != nil {
			return Response{Error: "failed", Message: err.Error()}
		}
		snap, _ := ctl.Snapshot(cctx)
		return Response{OK: true, State: &snap}
	}
	return Response{Error: "bad_request", Message: "unknown command " + req.Cmd}
}

func errorCode(err error) string {
	switch {
	case errors.Is(err, displays.ErrNoWlMirror):
		return "no_wl_mirror"
	case errors.Is(err, displays.ErrNoInternal), errors.Is(err, displays.ErrNoExternal),
		errors.Is(err, displays.ErrNeedTwo), errors.Is(err, displays.ErrNoDisplays):
		return "unavailable"
	}
	return "failed"
}

// Call sends one request to a running daemon.
func Call(socket string, req Request) (Response, error) {
	c, err := net.DialTimeout("unix", socket, 2*time.Second)
	if err != nil {
		return Response{}, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(30 * time.Second))
	b, _ := json.Marshal(req)
	if _, err := c.Write(append(b, '\n')); err != nil {
		return Response{}, err
	}
	line, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return Response{}, err
	}
	var r Response
	if err := json.Unmarshal(line, &r); err != nil {
		return Response{}, err
	}
	return r, nil
}
