package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Hints are the advice put into errors when the other side does not answer
// or goes away.
type Hints struct {
	Timeout      string
	Disconnected string
}

// Peer runs request/response calls over one WebSocket connection, either
// the editor plugin's (dialled by Client) or a running game's (accepted by
// the game package). Requests carry an id, so answers may come in any order.
type Peer struct {
	conn  *websocket.Conn
	who   string // "the editor", "the game"; used in error messages
	hints Hints

	mu      sync.Mutex
	nextID  int
	pending map[string]chan response
	done    chan struct{}
}

// NewPeer starts reading answers from conn, which has finished its handshake.
func NewPeer(conn *websocket.Conn, who string, hints Hints) *Peer {
	p := &Peer{conn: conn, who: who, hints: hints, pending: map[string]chan response{}, done: make(chan struct{})}
	go p.readLoop()
	return p
}

// Done is closed when the connection is gone.
func (p *Peer) Done() <-chan struct{} { return p.done }

// Alive reports whether the connection is still open.
func (p *Peer) Alive() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

func (p *Peer) readLoop() {
	for {
		_, data, err := p.conn.Read(context.Background())
		if err != nil {
			p.fail(err)
			return
		}
		var r response
		if json.Unmarshal(data, &r) != nil || r.ID == "" {
			continue
		}
		p.mu.Lock()
		ch := p.pending[r.ID]
		delete(p.pending, r.ID)
		p.mu.Unlock()
		if ch != nil {
			ch <- r
		}
	}
}

// fail closes the connection and fails every request waiting on it.
func (p *Peer) fail(cause error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.Alive() {
		return
	}
	close(p.done)
	p.conn.CloseNow()
	for id, ch := range p.pending {
		ch <- response{ID: id, Error: p.disconnected(cause)}
		delete(p.pending, id)
	}
}

func (p *Peer) disconnected(cause error) *Error {
	return &Error{Code: "DISCONNECTED", Message: fmt.Sprintf("%s closed the connection: %v", p.who, cause), Hint: p.hints.Disconnected}
}

// Call sends a command and waits for its answer. A failure on the other side
// is returned as *Error.
func (p *Peer) Call(ctx context.Context, command string, params any, timeout time.Duration) (json.RawMessage, error) {
	if params == nil {
		params = map[string]any{}
	}
	p.mu.Lock()
	if !p.Alive() {
		p.mu.Unlock()
		return nil, p.disconnected(errors.New("connection already closed"))
	}
	p.nextID++
	id := strconv.Itoa(p.nextID)
	ch := make(chan response, 1)
	p.pending[id] = ch
	p.mu.Unlock()

	msg, err := json.Marshal(map[string]any{"id": id, "command": command, "params": params})
	if err != nil {
		p.forget(id)
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := p.conn.Write(ctx, websocket.MessageText, msg); err != nil {
		p.forget(id)
		p.fail(err)
		return nil, p.disconnected(err)
	}
	select {
	case r := <-ch:
		if r.Error != nil {
			return nil, r.Error
		}
		if !r.OK {
			return nil, &Error{Code: "BAD_RESPONSE", Message: p.who + " answered without ok or error"}
		}
		return r.Data, nil
	case <-ctx.Done():
		p.forget(id)
		return nil, &Error{
			Code:    "TIMEOUT",
			Message: fmt.Sprintf("%s got no answer from %s within %s", command, p.who, timeout),
			Hint:    p.hints.Timeout,
		}
	}
}

func (p *Peer) forget(id string) {
	p.mu.Lock()
	delete(p.pending, id)
	p.mu.Unlock()
}

// Close ends the connection.
func (p *Peer) Close() {
	p.fail(errors.New("closed by the server"))
}
