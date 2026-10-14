// Package mirror supervises wl-mirror processes.
//
// niri has no native output mirroring, so "duplicated screens" is implemented
// by running `wl-mirror --fullscreen-output TARGET SOURCE` once per target.
// Every process is tied to the daemon's lifetime (PDEATHSIG) so a crash of the
// daemon or of the shell never leaves stale full-screen windows behind.
package mirror

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"syscall"
	"time"
)

// TitlePrefix is the window title given to every mirror window, followed by
// the target output name. Users can match it in niri window rules.
const TitlePrefix = "dms-displayswitch-mirror:"

const (
	maxQuickCrashes = 3
	quickCrash      = 3 * time.Second
)

type spawnReq struct {
	cmd *exec.Cmd
	res chan error
}

// Supervisor runs and watches the wl-mirror processes.
type Supervisor struct {
	bin     string
	spawnCh chan spawnReq

	mu      sync.Mutex
	source  string
	targets []string
	cancel  context.CancelFunc
	alive   map[string]bool
	cmds    map[string]*exec.Cmd
	gen     int

	// OnFailure is called (from a goroutine, no locks held) when a mirror
	// process kept crashing and was abandoned.
	OnFailure func()
}

// Locate returns the wl-mirror binary path, or "" if it cannot be found.
func Locate(explicit string) string {
	name := "wl-mirror"
	if explicit != "" {
		name = explicit
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return p
}

func New(bin string) *Supervisor {
	s := &Supervisor{bin: bin, spawnCh: make(chan spawnReq), alive: map[string]bool{}}
	go s.spawnLoop()
	return s
}

// spawnLoop starts every child from one dedicated, never-released OS thread.
// PR_SET_PDEATHSIG fires when the *thread* that forked exits, not the process,
// so forking from arbitrary goroutines could kill mirrors at random.
func (s *Supervisor) spawnLoop() {
	runtime.LockOSThread()
	for r := range s.spawnCh {
		r.res <- r.cmd.Start()
	}
}

func (s *Supervisor) Available() bool { return s.bin != "" }

func (s *Supervisor) command(source, target string) *exec.Cmd {
	cmd := exec.Command(s.bin,
		"--fullscreen-output", target,
		"--title", TitlePrefix+target,
		source)
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
	return cmd
}

func (s *Supervisor) startCmd(source, target string) (*exec.Cmd, error) {
	cmd := s.command(source, target)
	res := make(chan error, 1)
	s.spawnCh <- spawnReq{cmd: cmd, res: res}
	if err := <-res; err != nil {
		return nil, err
	}
	return cmd, nil
}

// Start (re)launches mirroring of source onto every target. It returns an
// error only if no mirror process at all could be started.
func (s *Supervisor) Start(source string, targets []string) error {
	if s.bin == "" {
		return errors.New("wl-mirror not found")
	}
	if source == "" || len(targets) == 0 {
		return errors.New("mirror needs a source and at least one target")
	}
	s.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.gen++
	gen := s.gen
	s.source, s.targets, s.cancel = source, append([]string(nil), targets...), cancel
	s.alive = map[string]bool{}
	s.cmds = map[string]*exec.Cmd{}
	s.mu.Unlock()

	started := 0
	var firstErr error
	for _, t := range targets {
		cmd, err := s.startCmd(source, t)
		if err != nil {
			log.Printf("mirror: start %s -> %s: %v", source, t, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		started++
		s.setProc(gen, t, cmd)
		go s.watch(ctx, gen, source, t, cmd)
	}
	if started == 0 {
		s.Stop()
		return fmt.Errorf("wl-mirror failed to start: %w", firstErr)
	}
	return nil
}

func (s *Supervisor) setAlive(gen int, target string, v bool) {
	s.mu.Lock()
	if s.gen == gen {
		s.alive[target] = v
	}
	s.mu.Unlock()
}

func (s *Supervisor) setProc(gen int, target string, cmd *exec.Cmd) {
	s.mu.Lock()
	if s.gen == gen {
		s.alive[target] = true
		s.cmds[target] = cmd
	}
	s.mu.Unlock()
}

// watch waits for the process and restarts it if it dies on its own. A process
// that keeps dying right after launch is abandoned.
func (s *Supervisor) watch(ctx context.Context, gen int, source, target string, cmd *exec.Cmd) {
	quick := 0
	for {
		began := time.Now()
		err := cmd.Wait()
		if ctx.Err() != nil {
			return
		}
		s.setAlive(gen, target, false)
		if time.Since(began) < quickCrash {
			quick++
		} else {
			quick = 0
		}
		log.Printf("mirror: wl-mirror for %s exited (%v)", target, err)
		if quick >= maxQuickCrashes {
			log.Printf("mirror: giving up on %s", target)
			if f := s.OnFailure; f != nil {
				go f()
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(quick+1) * 700 * time.Millisecond):
		}
		next, err := s.startCmd(source, target)
		if err != nil {
			log.Printf("mirror: restart %s: %v", target, err)
			if f := s.OnFailure; f != nil {
				go f()
			}
			return
		}
		cmd = next
		s.setProc(gen, target, cmd)
	}
}

// Stop terminates every mirror process (SIGTERM to the process group, SIGKILL
// after a grace period) without blocking.
func (s *Supervisor) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	cmds := s.cmds
	s.cancel, s.cmds = nil, nil
	s.source, s.targets = "", nil
	s.alive = map[string]bool{}
	s.gen++
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	for _, cmd := range cmds {
		if cmd == nil || cmd.Process == nil {
			continue
		}
		pid := cmd.Process.Pid
		_ = syscall.Kill(-pid, syscall.SIGTERM)
		time.AfterFunc(1500*time.Millisecond, func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
	}
}

// Running reports whether mirroring is active with every process alive.
func (s *Supervisor) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.targets) == 0 {
		return false
	}
	for _, t := range s.targets {
		if !s.alive[t] {
			return false
		}
	}
	return true
}

// Matches reports whether the given mirror plan is already what runs.
func (s *Supervisor) Matches(source string, targets []string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.source != source || len(s.targets) != len(targets) {
		return false
	}
	m := map[string]bool{}
	for _, t := range s.targets {
		m[t] = true
	}
	for _, t := range targets {
		if !m[t] {
			return false
		}
	}
	return true
}

// Active reports whether a mirror session is configured (even if a process
// is momentarily restarting).
func (s *Supervisor) Active() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.targets) > 0
}
