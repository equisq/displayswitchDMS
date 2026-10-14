// Package niri is a minimal client for niri's IPC socket ($NIRI_SOCKET).
//
// Protocol (https://github.com/niri-wm/niri/wiki/IPC): one JSON request per
// line, one JSON reply per line shaped {"Ok": ...} or {"Err": "..."}. The
// "EventStream" request then keeps the connection open and streams one JSON
// event per line.
package niri

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"time"
)

// Output is a connected output as reported by the Outputs request.
// An output that is turned off stays listed, with CurrentMode == nil.
type Output struct {
	Name        string   `json:"name"`
	Make        string   `json:"make"`
	Model       string   `json:"model"`
	Serial      *string  `json:"serial"`
	Modes       []Mode   `json:"modes"`
	CurrentMode *int     `json:"current_mode"`
	Logical     *Logical `json:"logical"`
}

type Mode struct {
	Width       int  `json:"width"`
	Height      int  `json:"height"`
	RefreshRate int  `json:"refresh_rate"` // mHz
	IsPreferred bool `json:"is_preferred"`
}

type Logical struct {
	X      int     `json:"x"`
	Y      int     `json:"y"`
	Width  int     `json:"width"`
	Height int     `json:"height"`
	Scale  float64 `json:"scale"`
}

// Enabled reports whether niri currently drives this output.
func (o Output) Enabled() bool { return o.CurrentMode != nil || o.Logical != nil }

type Window struct {
	ID          uint64  `json:"id"`
	Title       *string `json:"title"`
	AppID       *string `json:"app_id"`
	WorkspaceID *uint64 `json:"workspace_id"`
	IsFocused   bool    `json:"is_focused"`
}

type Workspace struct {
	ID     uint64  `json:"id"`
	Output *string `json:"output"`
}

// Event is one line of the event stream. Kind is the externally tagged
// variant name (e.g. "WorkspacesChanged", "ConfigLoaded", "OutputsChanged").
type Event struct {
	Kind string
	Raw  json.RawMessage
}

// ErrNoSocket is returned when niri is not running (or NIRI_SOCKET is unset).
var ErrNoSocket = errors.New("NIRI_SOCKET is not set: not running under niri")

type Client struct {
	Path    string
	Timeout time.Duration
}

func New() (*Client, error) {
	p := os.Getenv("NIRI_SOCKET")
	if p == "" {
		return nil, ErrNoSocket
	}
	return &Client{Path: p, Timeout: 4 * time.Second}, nil
}

type reply struct {
	Ok  json.RawMessage `json:"Ok"`
	Err *string         `json:"Err"`
}

func (c *Client) dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	d.Timeout = c.Timeout
	return d.DialContext(ctx, "unix", c.Path)
}

// do sends req and returns the raw "Ok" payload.
func (c *Client) do(ctx context.Context, req any) (json.RawMessage, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, fmt.Errorf("niri socket: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(c.Timeout))

	b, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return nil, fmt.Errorf("niri write: %w", err)
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return nil, fmt.Errorf("niri read: %w", err)
	}
	var r reply
	if err := json.Unmarshal(line, &r); err != nil {
		return nil, fmt.Errorf("niri reply: %w", err)
	}
	if r.Err != nil {
		return nil, fmt.Errorf("niri: %s", *r.Err)
	}
	return r.Ok, nil
}

// Outputs lists all connected outputs, sorted by name.
func (c *Client) Outputs(ctx context.Context) ([]Output, error) {
	raw, err := c.do(ctx, "Outputs")
	if err != nil {
		return nil, err
	}
	var wrap struct {
		Outputs map[string]Output `json:"Outputs"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil {
		return nil, fmt.Errorf("niri outputs: %w", err)
	}
	out := make([]Output, 0, len(wrap.Outputs))
	for name, o := range wrap.Outputs {
		if o.Name == "" {
			o.Name = name
		}
		out = append(out, o)
	}
	sortOutputs(out)
	return out, nil
}

// SetOutput turns an output on or off. The change is temporary (niri forgets
// it when the config file is reloaded or the output is re-plugged).
func (c *Client) SetOutput(ctx context.Context, name string, enabled bool) error {
	action := "Off"
	if enabled {
		action = "On"
	}
	_, err := c.do(ctx, map[string]any{"Output": map[string]any{"output": name, "action": action}})
	return err
}

func (c *Client) Windows(ctx context.Context) ([]Window, error) {
	raw, err := c.do(ctx, "Windows")
	if err != nil {
		return nil, err
	}
	var wrap struct {
		Windows []Window `json:"Windows"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil {
		return nil, err
	}
	return wrap.Windows, nil
}

func (c *Client) Workspaces(ctx context.Context) ([]Workspace, error) {
	raw, err := c.do(ctx, "Workspaces")
	if err != nil {
		return nil, err
	}
	var wrap struct {
		Workspaces []Workspace `json:"Workspaces"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil {
		return nil, err
	}
	return wrap.Workspaces, nil
}

func (c *Client) FocusedWindowID(ctx context.Context) (uint64, bool, error) {
	raw, err := c.do(ctx, "FocusedWindow")
	if err != nil {
		return 0, false, err
	}
	var wrap struct {
		FocusedWindow *Window `json:"FocusedWindow"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil {
		return 0, false, err
	}
	if wrap.FocusedWindow == nil {
		return 0, false, nil
	}
	return wrap.FocusedWindow.ID, true, nil
}

func (c *Client) FocusWindow(ctx context.Context, id uint64) error {
	_, err := c.do(ctx, map[string]any{"Action": map[string]any{"FocusWindow": map[string]any{"id": id}}})
	return err
}

func (c *Client) MoveWindowToMonitor(ctx context.Context, id uint64, output string) error {
	_, err := c.do(ctx, map[string]any{"Action": map[string]any{
		"MoveWindowToMonitor": map[string]any{"id": id, "output": output}}})
	return err
}

// Events opens the event stream. The returned channel is closed when the
// connection drops or ctx is cancelled; callers should reconnect.
func (c *Client) Events(ctx context.Context) (<-chan Event, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, fmt.Errorf("niri socket: %w", err)
	}
	_ = conn.SetWriteDeadline(time.Now().Add(c.Timeout))
	if _, err := conn.Write([]byte("\"EventStream\"\n")); err != nil {
		conn.Close()
		return nil, err
	}
	_ = conn.SetWriteDeadline(time.Time{})

	rd := bufio.NewReaderSize(conn, 1<<20)
	_ = conn.SetReadDeadline(time.Now().Add(c.Timeout))
	first, err := rd.ReadBytes('\n')
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("niri event stream handshake: %w", err)
	}
	var r reply
	if err := json.Unmarshal(first, &r); err != nil || r.Err != nil {
		conn.Close()
		return nil, fmt.Errorf("niri event stream refused: %s", string(first))
	}
	_ = conn.SetReadDeadline(time.Time{})

	ch := make(chan Event, 64)
	go func() {
		defer close(ch)
		defer conn.Close()
		go func() { <-ctx.Done(); conn.Close() }()
		for {
			line, err := rd.ReadBytes('\n')
			if len(line) > 0 {
				if ev, ok := parseEvent(line); ok {
					select {
					case ch <- ev:
					case <-ctx.Done():
						return
					}
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return ch, nil
}

func parseEvent(line []byte) (Event, bool) {
	var asString string
	if json.Unmarshal(line, &asString) == nil && asString != "" {
		return Event{Kind: asString}, true
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(line, &m); err != nil || len(m) == 0 {
		return Event{}, false
	}
	for k, v := range m {
		return Event{Kind: k, Raw: v}, true
	}
	return Event{}, false
}
