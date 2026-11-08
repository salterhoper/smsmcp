// Package pairing issues pairing links and handles POST /v1/pair.
//
// Flow: `smsmcp pair` stores the hash of a single-use token and prints a QR
// with smsmcp://pair?url=<server>&token=<token>. The phone scans it, the user
// picks a SIM/number, and the app calls POST /v1/pair. The server creates a
// device in pending_verification and returns a device token plus a code. The
// phone then texts "smsmcp verify <code>" to its own number; when that SMS
// arrives back on the same phone the hub activates the device.
package pairing

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/salterhoper/smsmcp/server/internal/ids"
	"github.com/salterhoper/smsmcp/server/internal/phone"
	"github.com/salterhoper/smsmcp/server/internal/store"
)

const TokenTTL = 10 * time.Minute

// NewLink creates a pairing token and returns the URI to encode in the QR.
func NewLink(ctx context.Context, st *store.Store, publicURL string) (string, error) {
	token := ids.Secret("smp", 24)
	if err := st.CreatePairingToken(ctx, ids.Hash(token), TokenTTL); err != nil {
		return "", err
	}
	q := url.Values{"url": {publicURL}, "token": {token}}
	return "smsmcp://pair?" + q.Encode(), nil
}

type pairRequest struct {
	Token             string `json:"token"`
	DeviceName        string `json:"device_name"`
	PhoneNumber       string `json:"phone_number"`
	SimSubscriptionID int    `json:"sim_subscription_id"`
}

type pairResponse struct {
	DeviceID      string `json:"device_id"`
	DeviceToken   string `json:"device_token"`
	PhoneNumber   string `json:"phone_number"`
	VerifyMessage string `json:"verify_message"`
	WSPath        string `json:"ws_path"`
}

// Handler serves POST /v1/pair.
func Handler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "use POST", http.StatusMethodNotAllowed)
			return
		}
		var req pairRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		num, err := phone.Normalize(req.PhoneNumber)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		name := strings.TrimSpace(req.DeviceName)
		if name == "" {
			name = "Android phone"
		}
		devToken := ids.Secret("smd", 32)
		code := ids.Digits(6)
		dev, err := st.PairDevice(r.Context(), store.NewDevice{
			PairingTokenHash: ids.Hash(req.Token), Name: name, PhoneNumber: num,
			SimSubscriptionID: req.SimSubscriptionID, DeviceTokenHash: ids.Hash(devToken), VerifyCode: code,
		})
		if errors.Is(err, store.ErrTokenExpired) {
			writeErr(w, http.StatusUnauthorized, err.Error())
			return
		} else if err != nil {
			writeErr(w, http.StatusInternalServerError, "could not create device")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(pairResponse{
			DeviceID: dev.ID, DeviceToken: devToken, PhoneNumber: num,
			VerifyMessage: "smsmcp verify " + code, WSPath: "/v1/device/ws",
		})
	}
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
