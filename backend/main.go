// displayswitch-dmsd: Super+P display switcher backend for DankMaterialShell on niri.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/equisq/displayswitchDMS/backend/internal/daemon"
	"github.com/equisq/displayswitchDMS/backend/internal/displays"
	"github.com/equisq/displayswitchDMS/backend/internal/mirror"
	"github.com/equisq/displayswitchDMS/backend/internal/niri"
)

var version = "dev"

// Exit codes (stable, the plugin relies on them).
const (
	exitOK          = 0
	exitFailed      = 1
	exitUsage       = 2
	exitNoDaemon    = 3
	exitUnavailable = 4
	exitNoWlMirror  = 5
	exitNotNiri     = 6
)

func usage() {
	fmt.Fprint(os.Stderr, `usage:
  displayswitch-dmsd daemon [flags]     run the watcher (started by the DMS plugin)
  displayswitch-dmsd set <mode>         internal | external | mirror | extend
  displayswitch-dmsd status             print the current state as JSON
  displayswitch-dmsd rescue             switch every connected display back on
  displayswitch-dmsd version

daemon flags:
  --exit-on-stdin-eof       quit when stdin closes (parent died)
  --wl-mirror PATH          wl-mirror binary (default: look in $PATH)
  --internal LIST           connector prefixes of built-in panels (default eDP,LVDS,DSI)
  --auto-on-connect MODE    extend (default) | keep
  --no-restore-on-start     do not restore the saved layout at startup
  --no-reassert             do not re-apply the layout after a niri config reload
`)
}

func runtimeDir() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return d
	}
	return os.TempDir()
}

func socketPath() string { return filepath.Join(runtimeDir(), "dms-displayswitch.sock") }
func lockPath() string   { return filepath.Join(runtimeDir(), "dms-displayswitch.lock") }

func statePath() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "DankMaterialShell", "displayswitch", "state.json")
}

func main() {
	log.SetFlags(log.Ltime)
	log.SetPrefix("displayswitch-dmsd: ")
	if len(os.Args) < 2 {
		usage()
		os.Exit(exitUsage)
	}
	switch os.Args[1] {
	case "daemon":
		os.Exit(cmdDaemon(os.Args[2:]))
	case "set":
		os.Exit(cmdSet(os.Args[2:]))
	case "status":
		os.Exit(cmdStatus())
	case "rescue":
		os.Exit(cmdRescue())
	case "version", "--version":
		fmt.Println(version)
	default:
		usage()
		os.Exit(exitUsage)
	}
}

func cmdDaemon(args []string) int {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	exitOnEOF := fs.Bool("exit-on-stdin-eof", false, "")
	wlm := fs.String("wl-mirror", "", "")
	internal := fs.String("internal", "", "")
	auto := fs.String("auto-on-connect", "extend", "")
	noRestore := fs.Bool("no-restore-on-start", false, "")
	noReassert := fs.Bool("no-reassert", false, "")
	_ = fs.Parse(args)

	nc, err := niri.New()
	if err != nil {
		log.Print(err)
		return exitNotNiri
	}

	var prefixes []string
	for _, p := range strings.Split(*internal, ",") {
		if p = strings.TrimSpace(p); p != "" {
			prefixes = append(prefixes, p)
		}
	}
	opt := displays.Options{
		InternalPrefixes:  prefixes,
		RestoreOnStart:    !*noRestore,
		StatePath:         statePath(),
		MirrorTitlePrefix: mirror.TitlePrefix,
	}
	if *auto == "extend" {
		opt.AutoOnConnect = displays.ModeExtend
	}

	sup := mirror.New(mirror.Locate(*wlm))
	ctl := displays.NewController(nc, sup, opt)
	// A wl-mirror that keeps crashing is abandoned: let the controller degrade.
	sup.OnFailure = func() { ctl.Reconcile(context.Background(), "mirror", false) }

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	err = daemon.Run(ctx, daemon.Config{
		Controller:       ctl,
		Niri:             nc,
		SocketPath:       socketPath(),
		LockPath:         lockPath(),
		Out:              os.Stdout,
		ExitOnStdinEOF:   *exitOnEOF,
		ReassertOnReload: !*noReassert,
		Stop:             sup.Stop,
	})
	if err != nil {
		log.Print(err)
		if err == daemon.ErrAlreadyRunning {
			return exitNoDaemon
		}
		return exitFailed
	}
	return exitOK
}

func cmdSet(args []string) int {
	if len(args) != 1 || !displays.Mode(args[0]).Valid() {
		usage()
		return exitUsage
	}
	return callAndPrint(daemon.Request{Cmd: "set", Mode: args[0]})
}

func cmdStatus() int { return callAndPrint(daemon.Request{Cmd: "status"}) }

func callAndPrint(req daemon.Request) int {
	resp, err := daemon.Call(socketPath(), req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "daemon not running (is the DisplaySwitch plugin enabled?):", err)
		return exitNoDaemon
	}
	b, _ := json.Marshal(resp)
	fmt.Println(string(b))
	if resp.OK {
		return exitOK
	}
	fmt.Fprintln(os.Stderr, resp.Error+":", resp.Message)
	switch resp.Error {
	case "unavailable":
		return exitUnavailable
	case "no_wl_mirror":
		return exitNoWlMirror
	}
	return exitFailed
}

// cmdRescue works with or without the daemon: it is the "my screen is black"
// button, so it talks to niri directly.
func cmdRescue() int {
	nc, err := niri.New()
	if err != nil {
		log.Print(err)
		return exitNotNiri
	}
	if resp, err := daemon.Call(socketPath(), daemon.Request{Cmd: "rescue"}); err == nil && resp.OK {
		fmt.Println("all displays enabled")
		return exitOK
	}
	ctl := displays.NewController(nc, nil, displays.Options{})
	if err := ctl.Rescue(context.Background()); err != nil {
		log.Print(err)
		return exitFailed
	}
	fmt.Println("all displays enabled")
	return exitOK
}
