// Package bridge talks to the godot_mcp editor plugin.
//
// The plugin listens on 127.0.0.1 and writes the port and a random token to
// <project>/.godot/godot_mcp/bridge.json. The server reads that file, dials
// in, proves it knows the token and then sends commands:
//
//	-> {"type":"auth","token":"...","protocol":1}
//	<- {"type":"hello","protocol":1,"godot_version":"4.7.2...","commands":[...]}
//	-> {"id":"7","command":"scene_tree","params":{...}}
//	<- {"id":"7","ok":true,"data":{...}}
//	<- {"id":"7","ok":false,"error":{"code":"NODE_NOT_FOUND","message":"...","hint":"..."}}
//
// The plugin is the listener so that several MCP servers (one per client
// session) can share one editor, and the editor never depends on a server
// being up.
package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Protocol is the bridge protocol version shared with the plugin.
const Protocol = 1

// Info is the content of bridge.json.
type Info struct {
	Protocol      int    `json:"protocol"`
	Port          int    `json:"port"`
	Token         string `json:"token"`
	PID           int    `json:"pid"`
	GodotVersion  string `json:"godot_version"`
	PluginVersion string `json:"plugin_version"`
	ProjectPath   string `json:"project_path"`
}

// Hello is the plugin's answer to a successful auth.
type Hello struct {
	Protocol      int      `json:"protocol"`
	GodotVersion  string   `json:"godot_version"`
	PluginVersion string   `json:"plugin_version"`
	Commands      []string `json:"commands"`
}

// Error is an error reported by the plugin. Code is stable; Hint says what
// to do about it.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

func (e *Error) Error() string {
	if e.Hint != "" {
		return fmt.Sprintf("%s: %s (hint: %s)", e.Code, e.Message, e.Hint)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// ErrNotConnected means no editor with the plugin is reachable.
var ErrNotConnected = errors.New("editor not connected")

type response struct {
	ID    string          `json:"id"`
	Type  string          `json:"type"`
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error *Error          `json:"error"`
	// hello fields
	Protocol      int      `json:"protocol"`
	GodotVersion  string   `json:"godot_version"`
	PluginVersion string   `json:"plugin_version"`
	Commands      []string `json:"commands"`
}

// editorHints go into errors about the editor connection.
var editorHints = Hints{
	Timeout:      "the editor may be busy importing, showing a modal dialog, or minimised; check the editor window",
	Disconnected: "the editor may have been closed or the plugin reloaded; call the tool again",
}

// Client is a lazily connected, self-reconnecting bridge client.
type Client struct {
	infoPath string

	mu    sync.Mutex
	peer  *Peer
	hello Hello
}

// New creates a client that reads bridge.json from mcpDir.
func New(mcpDir string) *Client {
	return &Client{infoPath: filepath.Join(mcpDir, "bridge.json")}
}

// ReadInfo reads bridge.json.
func (c *Client) ReadInfo() (Info, error) {
	var info Info
	b, err := os.ReadFile(c.infoPath)
	if err != nil {
		return info, err
	}
	err = json.Unmarshal(b, &info)
	return info, err
}

// Connected returns the handshake data when a connection is open.
func (c *Client) Connected() (Hello, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hello, c.peer != nil && c.peer.Alive()
}

// Connect opens the connection if it is not open yet.
func (c *Client) Connect(ctx context.Context) (Hello, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.peer != nil && c.peer.Alive() {
		return c.hello, nil
	}
	info, err := c.usableInfo()
	if err != nil {
		return Hello{}, err
	}
	conn, err := dial(ctx, info.Port)
	if err != nil {
		return Hello{}, err
	}
	hello, err := handshake(ctx, conn, info.Token)
	if err != nil {
		conn.CloseNow()
		return Hello{}, err
	}
	c.peer = NewPeer(conn, "the editor", editorHints)
	c.hello = hello
	return hello, nil
}

// usableInfo reads bridge.json and checks that it speaks our protocol.
func (c *Client) usableInfo() (Info, error) {
	info, err := c.ReadInfo()
	if errors.Is(err, os.ErrNotExist) {
		return info, fmt.Errorf("%w: %s does not exist, so the godot_mcp plugin is not running for this project", ErrNotConnected, c.infoPath)
	}
	if err != nil {
		return info, fmt.Errorf("%w: reading %s: %v", ErrNotConnected, c.infoPath, err)
	}
	if info.Protocol != Protocol {
		return info, fmt.Errorf("%w: the plugin speaks bridge protocol %d but this server speaks %d; update both to the same release", ErrNotConnected, info.Protocol, Protocol)
	}
	return info, nil
}

func dial(ctx context.Context, port int) (*websocket.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	// 127.0.0.1, never "localhost": localhost can resolve to ::1 first while
	// the plugin listens on IPv4 only.
	conn, _, err := websocket.Dial(ctx, "ws://127.0.0.1:"+strconv.Itoa(port), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: nothing answers on 127.0.0.1:%d (bridge.json may be left over from an editor that crashed): %v", ErrNotConnected, port, err)
	}
	conn.SetReadLimit(64 << 20) // screenshots are large
	return conn, nil
}

// handshake proves the token and reads the plugin's hello.
func handshake(ctx context.Context, conn *websocket.Conn, token string) (Hello, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	auth, _ := json.Marshal(map[string]any{"type": "auth", "token": token, "protocol": Protocol})
	if err := conn.Write(ctx, websocket.MessageText, auth); err != nil {
		return Hello{}, fmt.Errorf("%w: sending auth: %v", ErrNotConnected, err)
	}
	_, data, err := conn.Read(ctx)
	if err != nil {
		return Hello{}, fmt.Errorf("%w: no handshake reply from the editor: %v", ErrNotConnected, err)
	}
	var r response
	if err := json.Unmarshal(data, &r); err != nil {
		return Hello{}, fmt.Errorf("%w: bad handshake reply: %v", ErrNotConnected, err)
	}
	if r.Type == "hello" {
		return Hello{Protocol: r.Protocol, GodotVersion: r.GodotVersion, PluginVersion: r.PluginVersion, Commands: r.Commands}, nil
	}
	if r.Error != nil {
		return Hello{}, fmt.Errorf("%w: %v", ErrNotConnected, r.Error)
	}
	return Hello{}, fmt.Errorf("%w: unexpected handshake reply %q", ErrNotConnected, r.Type)
}

// Call sends a command and waits for its answer. It connects first when
// needed. A plugin-side failure is returned as *Error.
func (c *Client) Call(ctx context.Context, command string, params any, timeout time.Duration) (json.RawMessage, error) {
	if _, err := c.Connect(ctx); err != nil {
		return nil, err
	}
	c.mu.Lock()
	p := c.peer
	c.mu.Unlock()
	return p.Call(ctx, command, params, timeout)
}

// Close closes the connection.
func (c *Client) Close() {
	c.mu.Lock()
	p := c.peer
	c.mu.Unlock()
	if p != nil {
		p.Close()
	}
}
