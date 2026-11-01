package game

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/orebron/godot-mcp/internal/bridge"
)

// dialAsGame connects to the link the way the bridge in a game does.
func dialAsGame(t *testing.T, l *Link, token string) *websocket.Conn {
	t.Helper()
	port := ""
	for _, kv := range l.Env() {
		if v, ok := strings.CutPrefix(kv, "GODOT_MCP_PORT="); ok {
			port = v
		}
		if v, ok := strings.CutPrefix(kv, "GODOT_MCP_TOKEN="); ok && token == "" {
			token = v
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws://127.0.0.1:"+port, nil)
	if err != nil {
		t.Fatal(err)
	}
	hello, _ := json.Marshal(map[string]any{"type": "hello", "token": token, "protocol": LinkProtocol, "godot_version": "4.7.2", "commands": []string{"echo"}})
	if err := c.Write(ctx, websocket.MessageText, hello); err != nil {
		t.Fatal(err)
	}
	return c
}

// serveEcho answers every command with its params, like a minimal bridge.
func serveEcho(c *websocket.Conn) {
	for {
		_, data, err := c.Read(context.Background())
		if err != nil {
			return
		}
		var req map[string]any
		json.Unmarshal(data, &req)
		b, _ := json.Marshal(map[string]any{"id": req["id"], "ok": true, "data": req["params"]})
		c.Write(context.Background(), websocket.MessageText, b)
	}
}

func TestLinkCall(t *testing.T) {
	l, err := NewLink()
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go serveEcho(dialAsGame(t, l, ""))

	data, err := l.Call(context.Background(), "echo", map[string]any{"x": 1}, time.Second)
	if err != nil || string(data) != `{"x":1}` {
		t.Fatalf("echo = %s, %v", data, err)
	}
	if !l.Connected() || l.Hello().GodotVersion != "4.7.2" {
		t.Errorf("connected %v, hello %+v", l.Connected(), l.Hello())
	}

	l.Close()
	if _, err := l.Call(context.Background(), "echo", nil, time.Second); !errors.Is(err, ErrNoGame) {
		t.Errorf("call after Close = %v, want ErrNoGame", err)
	}
}

func TestLinkRejectsWrongToken(t *testing.T) {
	l, err := NewLink()
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	c := dialAsGame(t, l, "guess")
	if _, _, err := c.Read(context.Background()); websocket.CloseStatus(err) != 4001 {
		t.Errorf("wrong token: read error %v, want close status 4001", err)
	}
	if l.Connected() {
		t.Error("a game with the wrong token counts as connected")
	}
}

func TestLinkGameGoesAway(t *testing.T) {
	l, err := NewLink()
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	c := dialAsGame(t, l, "")
	// Wait for the link to accept the game, then let the game quit mid-call.
	deadline := time.Now().Add(5 * time.Second)
	for !l.Connected() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	go func() {
		c.Read(context.Background()) // the command arrives
		c.CloseNow()
	}()
	_, err = l.Call(context.Background(), "echo", nil, 5*time.Second)
	var be *bridge.Error
	if !errors.As(err, &be) || be.Code != "DISCONNECTED" || !strings.Contains(be.Hint, "game_output") {
		t.Errorf("call while the game quits = %v", err)
	}
}
