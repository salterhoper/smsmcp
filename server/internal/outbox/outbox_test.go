package outbox

import "testing"

// Vector from the Standard Webhooks reference implementation.
func TestSignStandardWebhooksVector(t *testing.T) {
	got, err := Sign("whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw", "msg_p5jXN8AQM9LWM0D4loKWxJek", 1614265330,
		[]byte(`{"test": 2432232314}`))
	if err != nil {
		t.Fatal(err)
	}
	want := "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE="
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}
