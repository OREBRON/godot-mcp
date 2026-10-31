package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fakePlugin mimics the editor plugin: it checks the token and answers
// commands with handle.
type fakePlugin struct {
	token  string
	handle func(cmd string, params map[string]any) map[string]any
	conns  chan *websocket.Conn
}

func (f *fakePlugin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	f.conns <- c
	ctx := context.Background()
	_, data, err := c.Read(ctx)
	if err != nil {
		return
	}
	var auth map[string]any
	json.Unmarshal(data, &auth)
	if auth["token"] != f.token {
		b, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]any{"code": "AUTH_FAILED", "message": "wrong token"}})
		c.Write(ctx, websocket.MessageText, b)
		c.Close(4001, "auth failed")
		return
	}
	b, _ := json.Marshal(map[string]any{"type": "hello", "protocol": Protocol, "godot_version": "4.7.2", "commands": []string{"echo"}})
	c.Write(ctx, websocket.MessageText, b)
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		var req struct {
			ID      string         `json:"id"`
			Command string         `json:"command"`
			Params  map[string]any `json:"params"`
		}
		json.Unmarshal(data, &req)
		go func() {
			resp := f.handle(req.Command, req.Params)
			if resp == nil {
				return // never answer
			}
			resp["id"] = req.ID
			b, _ := json.Marshal(resp)
			c.Write(ctx, websocket.MessageText, b)
		}()
	}
}

func startFake(t *testing.T, token string) (*fakePlugin, string) {
	t.Helper()
	f := &fakePlugin{token: token, conns: make(chan *websocket.Conn, 10), handle: func(cmd string, p map[string]any) map[string]any {
		switch cmd {
		case "echo":
			return map[string]any{"ok": true, "data": p}
		case "silent":
			return nil
		default:
			return map[string]any{"ok": false, "error": map[string]any{"code": "UNKNOWN_COMMAND", "message": cmd, "hint": "update"}}
		}
	}}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: f}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	dir := t.TempDir()
	info, _ := json.Marshal(Info{Protocol: Protocol, Port: ln.Addr().(*net.TCPAddr).Port, Token: token})
	os.WriteFile(filepath.Join(dir, "bridge.json"), info, 0o600)
	return f, dir
}

func TestCall(t *testing.T) {
	_, dir := startFake(t, "secret")
	c := New(dir)
	defer c.Close()
	ctx := context.Background()

	data, err := c.Call(ctx, "echo", map[string]any{"x": 1}, time.Second)
	if err != nil || string(data) != `{"x":1}` {
		t.Fatalf("echo = %s, %v", data, err)
	}
	if hello, ok := c.Connected(); !ok || hello.GodotVersion != "4.7.2" {
		t.Errorf("Connected = %+v, %v", hello, ok)
	}

	_, err = c.Call(ctx, "nope", nil, time.Second)
	var be *Error
	if !errors.As(err, &be) || be.Code != "UNKNOWN_COMMAND" || be.Hint != "update" {
		t.Errorf("unknown command error = %v", err)
	}

	_, err = c.Call(ctx, "silent", nil, 200*time.Millisecond)
	if !errors.As(err, &be) || be.Code != "TIMEOUT" {
		t.Errorf("silent command error = %v", err)
	}
	// The connection survives a timed-out command.
	if _, err := c.Call(ctx, "echo", nil, time.Second); err != nil {
		t.Errorf("echo after timeout: %v", err)
	}
}

func TestWrongToken(t *testing.T) {
	_, dir := startFake(t, "secret")
	info, _ := os.ReadFile(filepath.Join(dir, "bridge.json"))
	os.WriteFile(filepath.Join(dir, "bridge.json"), []byte(strings.Replace(string(info), "secret", "guess", 1)), 0o600)

	_, err := New(dir).Call(context.Background(), "echo", nil, time.Second)
	if !errors.Is(err, ErrNotConnected) || !strings.Contains(err.Error(), "AUTH_FAILED") {
		t.Errorf("wrong token error = %v", err)
	}
}

func TestReconnect(t *testing.T) {
	f, dir := startFake(t, "secret")
	c := New(dir)
	defer c.Close()
	if _, err := c.Call(context.Background(), "echo", nil, time.Second); err != nil {
		t.Fatal(err)
	}
	// The editor drops the connection, e.g. because the plugin reloaded.
	(<-f.conns).CloseNow()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, ok := c.Connected(); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("client did not notice the dropped connection")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := c.Call(context.Background(), "echo", nil, time.Second); err != nil {
		t.Errorf("call after reconnect: %v", err)
	}
}

func TestNoEditor(t *testing.T) {
	dir := t.TempDir()
	_, err := New(dir).Call(context.Background(), "echo", nil, time.Second)
	if !errors.Is(err, ErrNotConnected) || !strings.Contains(err.Error(), "not running") {
		t.Errorf("missing bridge.json error = %v", err)
	}

	// A bridge.json left behind by a crashed editor points at a dead port.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	info, _ := json.Marshal(Info{Protocol: Protocol, Port: port, Token: "x"})
	os.WriteFile(filepath.Join(dir, "bridge.json"), info, 0o600)
	_, err = New(dir).Call(context.Background(), "echo", nil, time.Second)
	if !errors.Is(err, ErrNotConnected) || !strings.Contains(err.Error(), "crashed") {
		t.Errorf("stale bridge.json error = %v", err)
	}
}
