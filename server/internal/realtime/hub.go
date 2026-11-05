// Package realtime fans game events out to websocket clients, applying
// per-seat visibility before anything is written to a socket.
package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"script-play/internal/domain"
	"script-play/internal/engine"
)

// Client is one websocket connection bound to a seat.
type Client struct {
	GameID string
	Seat   domain.Seat
	send   chan domain.Event
	closed chan struct{}
	once   sync.Once
}

// Close stops the write pump.
func (c *Client) Close() {
	c.once.Do(func() { close(c.closed) })
}

// Hub tracks connections per game.
type Hub struct {
	mu    sync.RWMutex
	games map[string]map[*Client]struct{}
}

// NewHub returns an empty hub.
func NewHub() *Hub {
	return &Hub{games: map[string]map[*Client]struct{}{}}
}

// Add registers a client for a game.
func (h *Hub) Add(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.games[c.GameID] == nil {
		h.games[c.GameID] = map[*Client]struct{}{}
	}
	h.games[c.GameID][c] = struct{}{}
}

// Remove unregisters a client.
func (h *Hub) Remove(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if set, ok := h.games[c.GameID]; ok {
		delete(set, c)
		if len(set) == 0 {
			delete(h.games, c.GameID)
		}
	}
}

// Count returns the number of live connections of a game.
func (h *Hub) Count(gameID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.games[gameID])
}

// Publish implements engine.Broadcaster. Events are filtered per client, so a
// private clue never reaches another socket.
func (h *Hub) Publish(ev domain.Event) {
	h.mu.RLock()
	set := h.games[ev.GameID]
	targets := make([]*Client, 0, len(set))
	for c := range set {
		if engine.EventVisibleTo(ev, &c.Seat) {
			targets = append(targets, c)
		}
	}
	h.mu.RUnlock()

	for _, c := range targets {
		select {
		case c.send <- ev:
		case <-c.closed:
		default:
			// Slow client: dropping is better than stalling a whole game.
			if ev.Type != domain.EvAgentDelta {
				slog.Warn("realtime: dropped event for slow client", "game", ev.GameID, "seat", c.Seat.ID, "type", ev.Type)
			}
		}
	}
}

// Handler processes one inbound frame.
type Handler func(ctx context.Context, seat domain.Seat, payload []byte) error

// Serve upgrades the request and pumps events until the socket closes.
func (h *Hub) Serve(ctx context.Context, w http.ResponseWriter, r *http.Request, seat domain.Seat, gameID string, onMessage Handler) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // the API is same-origin in dev; JWT is the real gate
		OriginPatterns:     []string{"*"},
	})
	if err != nil {
		slog.Warn("realtime: accept failed", "err", err)
		return
	}
	conn.SetReadLimit(64 * 1024)
	defer conn.Close(websocket.StatusNormalClosure, "bye")

	client := &Client{
		GameID: gameID,
		Seat:   seat,
		send:   make(chan domain.Event, 256),
		closed: make(chan struct{}),
	}
	h.Add(client)
	defer h.Remove(client)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	writeErr := make(chan error, 1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				writeErr <- nil
				return
			case <-client.closed:
				writeErr <- nil
				return
			case ev := <-client.send:
				b, err := json.Marshal(ev)
				if err != nil {
					continue
				}
				wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
				err = conn.Write(wctx, websocket.MessageText, b)
				wcancel()
				if err != nil {
					writeErr <- err
					return
				}
			}
		}
	}()

	for {
		select {
		case err := <-writeErr:
			if err != nil && !errors.Is(err, context.Canceled) {
				slog.Debug("realtime: write loop ended", "err", err)
			}
			return
		default:
		}
		_, data, err := conn.Read(ctx)
		if err != nil {
			client.Close()
			return
		}
		if onMessage != nil {
			if err := onMessage(ctx, seat, data); err != nil {
				payload, _ := json.Marshal(map[string]string{"type": "error", "message": err.Error()})
				wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
				_ = conn.Write(wctx, websocket.MessageText, payload)
				wcancel()
			}
		}
	}
}

// SendTo writes a synthetic event to every connection of a seat. Used for the
// initial state push right after connect.
func (h *Hub) SendTo(c *Client, ev domain.Event) {
	select {
	case c.send <- ev:
	default:
	}
}

// NewClientFor open a client handle without a connection (tests).
func NewClientFor(gameID string, seat domain.Seat) *Client {
	return &Client{GameID: gameID, Seat: seat, send: make(chan domain.Event, 8), closed: make(chan struct{})}
}
