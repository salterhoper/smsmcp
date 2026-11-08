package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/salterhoper/smsmcp/server/internal/ids"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// activeDevice pairs and activates a device, returning its id.
func activeDevice(t *testing.T, st *Store) string {
	t.Helper()
	ctx := context.Background()
	tok := "smp_test"
	if err := st.CreatePairingToken(ctx, ids.Hash(tok), time.Minute); err != nil {
		t.Fatal(err)
	}
	d, err := st.PairDevice(ctx, NewDevice{PairingTokenHash: ids.Hash(tok), Name: "p", PhoneNumber: "+255700000001",
		DeviceTokenHash: ids.Hash("smd_x"), VerifyCode: "123456"})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := st.ActivateDevice(ctx, d.ID); err != nil || !ok {
		t.Fatalf("activate: %v %v", ok, err)
	}
	return d.ID
}

func TestPairingTokenSingleUse(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	activeDevice(t, st)
	_, err := st.PairDevice(ctx, NewDevice{PairingTokenHash: ids.Hash("smp_test"), Name: "again",
		PhoneNumber: "+1", DeviceTokenHash: ids.Hash("smd_y")})
	if !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("second pair with same token: got %v, want ErrTokenExpired", err)
	}
}

func TestSendRequiresActiveDevice(t *testing.T) {
	st := newStore(t)
	_, _, err := st.CreateOutboundMessage(context.Background(), CreateOutbound{APIKeyID: "k", IdempotencyKey: "a", To: "+1555", Body: "hi"})
	if !errors.Is(err, ErrNoDevice) {
		t.Fatalf("got %v, want ErrNoDevice", err)
	}
}

func TestOutboundIdempotency(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	activeDevice(t, st)
	in := CreateOutbound{APIKeyID: "k1", IdempotencyKey: "order-42", To: "+255711111111", Body: "Your order shipped"}

	m1, created1, err := st.CreateOutboundMessage(ctx, in)
	if err != nil || !created1 {
		t.Fatalf("first: created=%v err=%v", created1, err)
	}
	m2, created2, err := st.CreateOutboundMessage(ctx, in)
	if err != nil || created2 || m2.ID != m1.ID {
		t.Fatalf("retry: created=%v id=%s want %s err=%v", created2, m2.ID, m1.ID, err)
	}

	// Same key, different body: refuse instead of silently returning the old one.
	in.Body = "Something else"
	if _, _, err := st.CreateOutboundMessage(ctx, in); !errors.Is(err, ErrIdemReused) {
		t.Fatalf("reused key: got %v, want ErrIdemReused", err)
	}

	// Same key under a different API key is a different send.
	in.Body, in.APIKeyID = "Your order shipped", "k2"
	m3, created3, err := st.CreateOutboundMessage(ctx, in)
	if err != nil || !created3 || m3.ID == m1.ID {
		t.Fatalf("other api key: created=%v err=%v", created3, err)
	}
}

func TestDispatchLeaseAndAccept(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	dev := activeDevice(t, st)
	m, _, err := st.CreateOutboundMessage(ctx, CreateOutbound{APIKeyID: "k", IdempotencyKey: "x", To: "+1555", Body: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	want := func(n int) {
		t.Helper()
		got, err := st.Dispatchable(ctx, dev, 10)
		if err != nil || len(got) != n {
			t.Fatalf("dispatchable: got %d want %d (err %v)", len(got), n, err)
		}
	}
	want(1)
	// Leased into the future: not dispatchable.
	st.MarkDispatched(ctx, m.ID, time.Now().Add(time.Minute).UnixMilli())
	want(0)
	// Lease expired without ack: dispatchable again.
	st.MarkDispatched(ctx, m.ID, time.Now().Add(-time.Second).UnixMilli())
	want(1)
	// Phone accepted it: never dispatched again, even with an old lease.
	if _, err := st.AdvanceStatus(ctx, m.ID, dev, StatusAccepted, ""); err != nil {
		t.Fatal(err)
	}
	want(0)
	st.MarkDispatched(ctx, m.ID, 0)
	if got, _ := st.GetMessage(ctx, m.ID); got.Status != StatusAccepted {
		t.Fatalf("MarkDispatched overwrote accepted: %s", got.Status)
	}
}

func TestStatusIsMonotonic(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	dev := activeDevice(t, st)
	m, _, _ := st.CreateOutboundMessage(ctx, CreateOutbound{APIKeyID: "k", IdempotencyKey: "x", To: "+1555", Body: "hi"})

	steps := []struct {
		status  string
		changed bool
	}{
		{StatusSending, true},
		{StatusDelivered, true},
		{StatusSent, false},      // late, out of order
		{StatusDelivered, false}, // repeat
		{StatusFailed, false},    // same rank as delivered
	}
	for _, s := range steps {
		changed, err := st.AdvanceStatus(ctx, m.ID, dev, s.status, "")
		if err != nil || changed != s.changed {
			t.Fatalf("%s: changed=%v want %v err=%v", s.status, changed, s.changed, err)
		}
	}
	if got, _ := st.GetMessage(ctx, m.ID); got.Status != StatusDelivered {
		t.Fatalf("final status %s", got.Status)
	}

	// Wrong device cannot touch the message.
	if changed, _ := st.AdvanceStatus(ctx, m.ID, "dev_other", StatusFailed, ""); changed {
		t.Fatal("status changed by another device")
	}
}

func TestUnknownCanResolveToSent(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	dev := activeDevice(t, st)
	m, _, _ := st.CreateOutboundMessage(ctx, CreateOutbound{APIKeyID: "k", IdempotencyKey: "x", To: "+1555", Body: "hi"})
	st.AdvanceStatus(ctx, m.ID, dev, StatusUnknown, "app restarted mid-send")
	if changed, _ := st.AdvanceStatus(ctx, m.ID, dev, StatusSent, ""); !changed {
		t.Fatal("late sent report should resolve unknown")
	}
	if changed, _ := st.AdvanceStatus(ctx, m.ID, dev, StatusUnknown, ""); changed {
		t.Fatal("unknown must not override sent")
	}
}

func TestInboundDedupAndOutbox(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	dev := activeDevice(t, st)
	if _, err := st.AddWebhookEndpoint(ctx, "http://example.invalid/hook"); err != nil {
		t.Fatal(err)
	}

	m1, dup1, err := st.InsertInbound(ctx, dev, "hash-1", "+255722222222", "hello", 1000)
	if err != nil || dup1 {
		t.Fatalf("first: dup=%v err=%v", dup1, err)
	}
	_, dup2, err := st.InsertInbound(ctx, dev, "hash-1", "+255722222222", "hello", 1000)
	if err != nil || !dup2 {
		t.Fatalf("repeat: dup=%v err=%v", dup2, err)
	}

	msgs, _ := st.ListMessages(ctx, ListFilter{Direction: "in"})
	if len(msgs) != 1 || msgs[0].ID != m1.ID {
		t.Fatalf("want exactly one inbound message, got %d", len(msgs))
	}
	due, err := st.DueDeliveries(ctx, 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("want exactly one webhook delivery, got %d (err %v)", len(due), err)
	}
}
