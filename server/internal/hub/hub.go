// Package hub keeps one WebSocket per paired phone and moves jobs and
// reports across it. The wire format is documented in docs/PROTOCOL.md.
//
// Delivery between server and phone is at-least-once in both directions:
//   - server -> phone "send" frames are re-pushed until the phone acks with
//     "accepted" (lease expiry in Store.Dispatchable). The phone dedupes on id.
//   - phone -> server "inbound" and "status" frames are re-sent until the
//     server acks them. The server dedupes via Store.InsertInbound and the
//     monotonic Store.AdvanceStatus.
package hub

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/salterhoper/smsmcp/server/internal/ids"
	"github.com/salterhoper/smsmcp/server/internal/phone"
	"github.com/salterhoper/smsmcp/server/internal/store"
)

// Frame is the envelope for every WebSocket message in both directions.
type Frame struct {
	Type string `json:"type"`

	// send (server->phone), status (phone->server), status_ack (server->phone)
	ID     string `json:"id,omitempty"`
	To     string `json:"to,omitempty"`
	Body   string `json:"body,omitempty"`
	Status string `json:"status,omitempty"`
	Error  string `json:"error,omitempty"`

	// inbound (phone->server), inbound_ack (server->phone)
	Dedup      string `json:"dedup,omitempty"`
	From       string `json:"from,omitempty"`
	ReceivedAt int64  `json:"received_at,omitempty"`

	// hello (server->phone)
	DeviceID     string `json:"device_id,omitempty"`
	DeviceStatus string `json:"device_status,omitempty"`

	// heartbeat (phone->server)
	Heartbeat *store.Heartbeat `json:"heartbeat,omitempty"`
}

type Hub struct {
	Store *store.Store
	Log   *slog.Logger
	// Lease is how long a dispatched job waits for an "accepted" ack before
	// it is pushed again.
	Lease time.Duration

	mu    sync.Mutex
	conns map[string]*conn
	wake  chan struct{}
}

type conn struct {
	deviceID string
	ws       *websocket.Conn
	wmu      sync.Mutex
}

func New(st *store.Store, log *slog.Logger) *Hub {
	return &Hub{Store: st, Log: log, Lease: 45 * time.Second, conns: map[string]*conn{}, wake: make(chan struct{}, 1)}
}

// Notify asks the dispatcher to run now (e.g. after a new message is queued).
func (h *Hub) Notify() {
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

func (h *Hub) Connected(deviceID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.conns[deviceID]
	return ok
}

// Run dispatches queued messages to connected phones until ctx ends. It polls
// every few seconds as well, so messages queued by another process sharing
// the database (e.g. `smsmcp stdio`) are picked up too.
func (h *Hub) Run(ctx context.Context) {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-h.wake:
		}
		h.mu.Lock()
		conns := make([]*conn, 0, len(h.conns))
		for _, c := range h.conns {
			conns = append(conns, c)
		}
		h.mu.Unlock()
		for _, c := range conns {
			h.dispatch(ctx, c)
		}
	}
}

func (h *Hub) dispatch(ctx context.Context, c *conn) {
	msgs, err := h.Store.Dispatchable(ctx, c.deviceID, 50)
	if err != nil {
		h.Log.Error("dispatchable", "err", err)
		return
	}
	for _, m := range msgs {
		// Mark first, then push: if the push fails the lease simply expires
		// and the message is pushed again on the next pass.
		if err := h.Store.MarkDispatched(ctx, m.ID, time.Now().Add(h.Lease).UnixMilli()); err != nil {
			h.Log.Error("mark dispatched", "id", m.ID, "err", err)
			continue
		}
		if err := c.write(ctx, Frame{Type: "send", ID: m.ID, To: m.PeerNumber, Body: m.Body}); err != nil {
			h.Log.Warn("push failed", "device", c.deviceID, "id", m.ID, "err", err)
			return
		}
	}
}

// ServeHTTP upgrades GET /v1/device/ws. The phone authenticates with its
// device token: "Authorization: Bearer smd_...".
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		http.Error(w, "missing device token", http.StatusUnauthorized)
		return
	}
	dev, err := h.Store.DeviceByTokenHash(r.Context(), ids.Hash(token))
	if err != nil {
		http.Error(w, "unknown or disabled device", http.StatusUnauthorized)
		return
	}
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	ws.SetReadLimit(256 << 10)
	c := &conn{deviceID: dev.ID, ws: ws}

	h.mu.Lock()
	if old, ok := h.conns[dev.ID]; ok {
		old.ws.Close(websocket.StatusPolicyViolation, "replaced by a newer connection")
	}
	h.conns[dev.ID] = c
	h.mu.Unlock()
	h.Log.Info("phone connected", "device", dev.ID, "name", dev.Name)

	defer func() {
		h.mu.Lock()
		if h.conns[dev.ID] == c {
			delete(h.conns, dev.ID)
		}
		h.mu.Unlock()
		ws.CloseNow()
		h.Log.Info("phone disconnected", "device", dev.ID)
	}()

	ctx := r.Context()
	_ = h.Store.TouchDevice(ctx, dev.ID, nil)
	if err := c.write(ctx, Frame{Type: "hello", DeviceID: dev.ID, DeviceStatus: dev.Status}); err != nil {
		return
	}
	h.Notify()

	go h.keepalive(ctx, c)
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var f Frame
		if err := json.Unmarshal(data, &f); err != nil {
			h.Log.Warn("bad frame", "device", dev.ID, "err", err)
			continue
		}
		if err := h.handle(ctx, c, f); err != nil {
			h.Log.Error("handle frame", "device", dev.ID, "type", f.Type, "err", err)
		}
	}
}

func (h *Hub) keepalive(ctx context.Context, c *conn) {
	t := time.NewTicker(25 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.ws.Ping(pctx)
			cancel()
			if err != nil {
				c.ws.Close(websocket.StatusGoingAway, "ping timeout")
				return
			}
		}
	}
}

func (h *Hub) handle(ctx context.Context, c *conn, f Frame) error {
	switch f.Type {
	case "status":
		if !store.ValidPhoneStatus(f.Status) {
			return errors.New("invalid status " + f.Status)
		}
		changed, err := h.Store.AdvanceStatus(ctx, f.ID, c.deviceID, f.Status, f.Error)
		if err != nil {
			return err
		}
		if changed {
			h.Log.Info("status", "id", f.ID, "status", f.Status)
		}
		// Ack even when unchanged: the phone needs it to stop resending.
		return c.write(ctx, Frame{Type: "status_ack", ID: f.ID, Status: f.Status})

	case "inbound":
		if f.Dedup == "" || f.From == "" {
			return errors.New("inbound frame needs dedup and from")
		}
		if err := h.handleInbound(ctx, c.deviceID, f); err != nil {
			return err
		}
		return c.write(ctx, Frame{Type: "inbound_ack", Dedup: f.Dedup})

	case "heartbeat":
		return h.Store.TouchDevice(ctx, c.deviceID, f.Heartbeat)

	default:
		return errors.New("unknown frame type " + f.Type)
	}
}

func (h *Hub) handleInbound(ctx context.Context, deviceID string, f Frame) error {
	dev, err := h.Store.GetDevice(ctx, deviceID)
	if err != nil {
		return err
	}
	// Number verification: the phone texts "smsmcp verify <code>" to its own
	// number. Receiving it from that number on that device proves the SIM.
	if dev.Status == store.DevicePending && dev.VerifyCode != "" &&
		strings.Contains(f.Body, "smsmcp verify "+dev.VerifyCode) && phone.SameNumber(f.From, dev.PhoneNumber) {
		if _, err := h.Store.ActivateDevice(ctx, dev.ID); err != nil {
			return err
		}
		h.Log.Info("device verified", "device", dev.ID, "number", dev.PhoneNumber)
		return h.Store.MarkInboundSeen(ctx, dev.ID, f.Dedup)
	}
	m, dup, err := h.Store.InsertInbound(ctx, deviceID, f.Dedup, f.From, f.Body, f.ReceivedAt)
	if err != nil {
		return err
	}
	if !dup {
		h.Log.Info("inbound sms", "id", m.ID, "from", m.PeerNumber)
	}
	return nil
}

func (c *conn) write(ctx context.Context, f Frame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return c.ws.Write(wctx, websocket.MessageText, b)
}
