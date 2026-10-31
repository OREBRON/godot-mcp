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

// Client is a lazily connected, self-reconnecting bridge client.
type Client struct {
	infoPath string

	mu      sync.Mutex
	conn    *websocket.Conn
	hello   Hello
	nextID  int
	pending map[string]chan response
}

// New creates a client that reads bridge.json from mcpDir.
func New(mcpDir string) *Client {
	return &Client{infoPath: filepath.Join(mcpDir, "bridge.json"), pending: map[string]chan response{}}
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
	return c.hello, c.conn != nil
}

// Connect opens the connection if it is not open yet.
func (c *Client) Connect(ctx context.Context) (Hello, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		return c.hello, nil
	}
	info, err := c.ReadInfo()
	if errors.Is(err, os.ErrNotExist) {
		return Hello{}, fmt.Errorf("%w: %s does not exist, so the godot_mcp plugin is not running for this project", ErrNotConnected, c.infoPath)
	}
	if err != nil {
		return Hello{}, fmt.Errorf("%w: reading %s: %v", ErrNotConnected, c.infoPath, err)
	}
	if info.Protocol != Protocol {
		return Hello{}, fmt.Errorf("%w: the plugin speaks bridge protocol %d but this server speaks %d; update both to the same release", ErrNotConnected, info.Protocol, Protocol)
	}

	dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	// 127.0.0.1, never "localhost": localhost can resolve to ::1 first while
	// the plugin listens on IPv4 only.
	conn, _, err := websocket.Dial(dctx, "ws://127.0.0.1:"+strconv.Itoa(info.Port), nil)
	if err != nil {
		return Hello{}, fmt.Errorf("%w: nothing answers on 127.0.0.1:%d (bridge.json may be left over from an editor that crashed): %v", ErrNotConnected, info.Port, err)
	}
	conn.SetReadLimit(64 << 20) // screenshots are large

	auth, _ := json.Marshal(map[string]any{"type": "auth", "token": info.Token, "protocol": Protocol})
	if err := conn.Write(dctx, websocket.MessageText, auth); err != nil {
		conn.CloseNow()
		return Hello{}, fmt.Errorf("%w: sending auth: %v", ErrNotConnected, err)
	}
	hctx, hcancel := context.WithTimeout(ctx, 5*time.Second)
	defer hcancel()
	_, data, err := conn.Read(hctx)
	if err != nil {
		conn.CloseNow()
		return Hello{}, fmt.Errorf("%w: no handshake reply from the editor: %v", ErrNotConnected, err)
	}
	var r response
	if err := json.Unmarshal(data, &r); err != nil {
		conn.CloseNow()
		return Hello{}, fmt.Errorf("%w: bad handshake reply: %v", ErrNotConnected, err)
	}
	if r.Type != "hello" {
		conn.CloseNow()
		if r.Error != nil {
			return Hello{}, fmt.Errorf("%w: %v", ErrNotConnected, r.Error)
		}
		return Hello{}, fmt.Errorf("%w: unexpected handshake reply %q", ErrNotConnected, r.Type)
	}
	c.conn = conn
	c.hello = Hello{Protocol: r.Protocol, GodotVersion: r.GodotVersion, PluginVersion: r.PluginVersion, Commands: r.Commands}
	go c.readLoop(conn)
	return c.hello, nil
}

func (c *Client) readLoop(conn *websocket.Conn) {
	for {
		_, data, err := conn.Read(context.Background())
		if err != nil {
			c.drop(conn, err)
			return
		}
		var r response
		if json.Unmarshal(data, &r) != nil || r.ID == "" {
			continue
		}
		c.mu.Lock()
		ch := c.pending[r.ID]
		delete(c.pending, r.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- r
		}
	}
}

// drop forgets conn and fails every request waiting on it.
func (c *Client) drop(conn *websocket.Conn, cause error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != conn {
		return
	}
	conn.CloseNow()
	c.conn = nil
	c.hello = Hello{}
	for id, ch := range c.pending {
		ch <- response{ID: id, Error: &Error{Code: "DISCONNECTED", Message: "the editor closed the connection: " + cause.Error(), Hint: "the editor may have been closed or the plugin reloaded; call the tool again"}}
		delete(c.pending, id)
	}
}

// Call sends a command and waits for its answer. It connects first when
// needed. A plugin-side failure is returned as *Error.
func (c *Client) Call(ctx context.Context, command string, params any, timeout time.Duration) (json.RawMessage, error) {
	if _, err := c.Connect(ctx); err != nil {
		return nil, err
	}
	if params == nil {
		params = map[string]any{}
	}
	c.mu.Lock()
	conn := c.conn
	if conn == nil {
		c.mu.Unlock()
		return nil, ErrNotConnected
	}
	c.nextID++
	id := strconv.Itoa(c.nextID)
	ch := make(chan response, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	msg, err := json.Marshal(map[string]any{"id": id, "command": command, "params": params})
	if err != nil {
		c.forget(id)
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, msg); err != nil {
		c.forget(id)
		c.drop(conn, err)
		return nil, fmt.Errorf("%w: sending %s: %v", ErrNotConnected, command, err)
	}
	select {
	case r := <-ch:
		if r.Error != nil {
			return nil, r.Error
		}
		if !r.OK {
			return nil, &Error{Code: "BAD_RESPONSE", Message: "the plugin answered without ok or error"}
		}
		return r.Data, nil
	case <-ctx.Done():
		c.forget(id)
		return nil, &Error{
			Code:    "TIMEOUT",
			Message: fmt.Sprintf("%s got no answer within %s", command, timeout),
			Hint:    "the editor may be busy importing, showing a modal dialog, or minimised; check the editor window",
		}
	}
}

func (c *Client) forget(id string) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// Close closes the connection.
func (c *Client) Close() {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn != nil {
		conn.Close(websocket.StatusNormalClosure, "bye")
	}
}
