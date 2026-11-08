package hub_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/salterhoper/smsmcp/server/internal/api"
	"github.com/salterhoper/smsmcp/server/internal/hub"
	"github.com/salterhoper/smsmcp/server/internal/mcpserver"
	"github.com/salterhoper/smsmcp/server/internal/pairing"
	"github.com/salterhoper/smsmcp/server/internal/store"
)

// fakePhone drives the device WebSocket the way the Android app does.
type fakePhone struct {
	t  *testing.T
	ws *websocket.Conn
}

func (p *fakePhone) send(f hub.Frame) {
	p.t.Helper()
	b, _ := json.Marshal(f)
	if err := p.ws.Write(context.Background(), websocket.MessageText, b); err != nil {
		p.t.Fatal(err)
	}
}

func (p *fakePhone) next(typ string) hub.Frame {
	p.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		_, data, err := p.ws.Read(ctx)
		if err != nil {
			p.t.Fatalf("waiting for %s: %v", typ, err)
		}
		var f hub.Frame
		json.Unmarshal(data, &f)
		if f.Type == typ {
			return f
		}
	}
}

func TestPhoneEndToEnd(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "e2e.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := hub.New(st, log)
	h.Lease = 300 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.Run(ctx)

	mcpSrv := &mcpserver.Server{Store: st, Notify: h}
	srv := httptest.NewServer(api.Router(api.Deps{Store: st, Hub: h, MCP: mcpSrv}))
	defer srv.Close()

	// 1. Pair with the token from the QR link.
	link, err := pairing.NewLink(ctx, st, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(link)
	body, _ := json.Marshal(map[string]any{"token": u.Query().Get("token"), "device_name": "Pixel",
		"phone_number": "+255 700 000 001", "sim_subscription_id": 2})
	resp, err := http.Post(srv.URL+"/v1/pair", "application/json", bytes.NewReader(body))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("pair: %v %v", err, resp.Status)
	}
	var pr struct {
		DeviceToken   string `json:"device_token"`
		VerifyMessage string `json:"verify_message"`
		DeviceID      string `json:"device_id"`
	}
	json.NewDecoder(resp.Body).Decode(&pr)
	resp.Body.Close()

	// 2. Connect the phone.
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/device/ws"
	ws, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + pr.DeviceToken}}})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	p := &fakePhone{t: t, ws: ws}
	if hello := p.next("hello"); hello.DeviceStatus != store.DevicePending {
		t.Fatalf("hello status %q", hello.DeviceStatus)
	}

	// 3. Verification SMS arrives back on the phone from its own number (local format).
	p.send(hub.Frame{Type: "inbound", Dedup: "v1", From: "0700000001", Body: pr.VerifyMessage})
	p.next("inbound_ack")
	if d, _ := st.GetDevice(ctx, pr.DeviceID); d.Status != store.DeviceActive {
		t.Fatalf("device not activated: %s", d.Status)
	}
	if msgs, _ := st.ListMessages(ctx, store.ListFilter{}); len(msgs) != 0 {
		t.Fatal("verification SMS should not appear as a message")
	}

	// 4. Agent sends; the phone gets a job, ignores it (simulated lost ack),
	//    and gets the same job again after the lease expires.
	m, _, err := st.CreateOutboundMessage(ctx, store.CreateOutbound{APIKeyID: "k", IdempotencyKey: "a", To: "+255711111111", Body: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	h.Notify()
	first := p.next("send")
	again := p.next("send")
	if first.ID != m.ID || again.ID != m.ID {
		t.Fatalf("redispatch ids: %s %s want %s", first.ID, again.ID, m.ID)
	}

	// 5. Phone accepts, then reports sent twice; every report is acked.
	for _, s := range []string{store.StatusAccepted, store.StatusSent, store.StatusSent, store.StatusDelivered} {
		p.send(hub.Frame{Type: "status", ID: m.ID, Status: s})
		if ack := p.next("status_ack"); ack.ID != m.ID || ack.Status != s {
			t.Fatalf("ack %+v", ack)
		}
	}
	if got, _ := st.GetMessage(ctx, m.ID); got.Status != store.StatusDelivered {
		t.Fatalf("status %s", got.Status)
	}

	// 6. Inbound SMS uploaded twice is stored once.
	for i := 0; i < 2; i++ {
		p.send(hub.Frame{Type: "inbound", Dedup: "in-1", From: "+255711111111", Body: "hello back", ReceivedAt: time.Now().UnixMilli()})
		p.next("inbound_ack")
	}
	if msgs, _ := st.ListMessages(ctx, store.ListFilter{Direction: "in"}); len(msgs) != 1 {
		t.Fatalf("inbound stored %d times", len(msgs))
	}

	// 7. Accepted jobs are not pushed again.
	time.Sleep(4 * time.Second) // > one dispatcher tick
	rctx, rcancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer rcancel()
	if _, data, err := ws.Read(rctx); err == nil && bytes.Contains(data, []byte(`"send"`)) {
		t.Fatalf("accepted job pushed again: %s", data)
	}
}

func TestMCPRequiresAPIKey(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "auth.db"))
	defer st.Close()
	h := hub.New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv := httptest.NewServer(api.Router(api.Deps{Store: st, Hub: h, MCP: &mcpserver.Server{Store: st}}))
	defer srv.Close()

	init := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
	post := func(key string) int {
		req, _ := http.NewRequest("POST", srv.URL+"/mcp", strings.NewReader(init))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := post(""); c != 401 {
		t.Fatalf("no key: %d", c)
	}
	if c := post("smk_wrong"); c != 401 {
		t.Fatalf("bad key: %d", c)
	}
	_, secret, _ := st.CreateAPIKey(context.Background(), "t")
	if c := post(secret); c != 200 {
		t.Fatalf("good key: %d", c)
	}
}
