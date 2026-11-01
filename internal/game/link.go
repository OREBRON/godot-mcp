package game

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/orebron/godot-mcp/internal/bridge"
)

// LinkProtocol is the protocol version between the server and the bridge
// that game_run puts into the game. Both ship in the same binary.
const LinkProtocol = 1

// connectWait is how long a call waits for a freshly started game to dial
// back; loading the first scene can take a few seconds.
const connectWait = 15 * time.Second

var gameHints = bridge.Hints{
	Timeout:      "the game may be stuck in a long frame; read game_output",
	Disconnected: "the game exited or closed the connection; read game_output",
}

// GameHello is what the bridge reports when it connects.
type GameHello struct {
	GodotVersion string   `json:"godot_version"`
	Headless     bool     `json:"headless"`
	Commands     []string `json:"commands"`
}

// Link is the server end of the connection to a game started by game_run.
// The server listens on 127.0.0.1 and passes the port and a random token to
// the game in its environment; the bridge node in the game dials back and
// proves it has the token. A game started any other way has neither, so its
// bridge stays off.
type Link struct {
	ln    net.Listener
	srv   *http.Server
	token string

	mu     sync.Mutex
	peer   *bridge.Peer
	hello  GameHello
	ready  chan struct{} // closed when the game has connected
	closed chan struct{}
	once   sync.Once
}

// NewLink starts listening for one game.
func NewLink() (*Link, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		ln.Close()
		return nil, err
	}
	l := &Link{ln: ln, token: hex.EncodeToString(b), ready: make(chan struct{}), closed: make(chan struct{})}
	l.srv = &http.Server{Handler: http.HandlerFunc(l.accept), ReadHeaderTimeout: 5 * time.Second}
	go l.srv.Serve(ln)
	return l, nil
}

// Env is what the game needs to find the server.
func (l *Link) Env() []string {
	port := l.ln.Addr().(*net.TCPAddr).Port
	return []string{"GODOT_MCP_PORT=" + strconv.Itoa(port), "GODOT_MCP_TOKEN=" + l.token}
}

func (l *Link) accept(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	c.SetReadLimit(64 << 20) // screenshots
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, data, err := c.Read(ctx)
	if err != nil {
		c.CloseNow()
		return
	}
	var h struct {
		Type     string `json:"type"`
		Token    string `json:"token"`
		Protocol int    `json:"protocol"`
		GameHello
	}
	if json.Unmarshal(data, &h) != nil || h.Type != "hello" || subtle.ConstantTimeCompare([]byte(h.Token), []byte(l.token)) != 1 {
		c.Close(4001, "auth failed")
		return
	}
	if h.Protocol != LinkProtocol {
		c.Close(4002, "protocol mismatch")
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	select {
	case <-l.closed:
		c.Close(websocket.StatusGoingAway, "run ended")
		return
	case <-l.ready:
		c.Close(4003, "a game is already connected")
		return
	default:
	}
	l.peer = bridge.NewPeer(c, "the game", gameHints)
	l.hello = h.GameHello
	close(l.ready)
}

// Connected reports whether the game is connected right now.
func (l *Link) Connected() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.peer != nil && l.peer.Alive()
}

// Hello returns what the game reported when it connected.
func (l *Link) Hello() GameHello {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.hello
}

// Call waits for the game to connect, then sends a command to its bridge.
func (l *Link) Call(ctx context.Context, command string, params any, timeout time.Duration) (json.RawMessage, error) {
	// Checked first: after a run ends both channels are closed, and select
	// would pick either.
	select {
	case <-l.closed:
		return nil, ErrNoGame
	default:
	}
	wait := time.NewTimer(connectWait)
	defer wait.Stop()
	select {
	case <-l.ready:
	case <-l.closed:
		return nil, ErrNoGame
	case <-wait.C:
		return nil, &bridge.Error{
			Code:    "GAME_NOT_CONNECTED",
			Message: "the game has not connected to the server",
			Hint:    "it may still be loading, or it failed before its first frame; read game_output",
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	l.mu.Lock()
	p := l.peer
	l.mu.Unlock()
	return p.Call(ctx, command, params, timeout)
}

// ErrNoGame is returned when no game started by game_run is running.
var ErrNoGame = errors.New("no game is running; start one with game_run")

// Close stops listening and drops the game's connection.
func (l *Link) Close() {
	l.once.Do(func() {
		l.mu.Lock()
		close(l.closed)
		p := l.peer
		l.mu.Unlock()
		l.srv.Close()
		if p != nil {
			p.Close()
		}
	})
}
