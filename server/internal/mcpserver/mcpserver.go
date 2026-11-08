// Package mcpserver exposes SMS as MCP tools for agents.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/salterhoper/smsmcp/server/internal/ids"
	"github.com/salterhoper/smsmcp/server/internal/phone"
	"github.com/salterhoper/smsmcp/server/internal/store"
)

// LocalKeyID is the API key identity used for stdio sessions, which are
// trusted because they run as the local user.
const LocalKeyID = "local"

// AutoKeyWindow is the window in which an identical send without an explicit
// idempotency key is treated as a retry.
const AutoKeyWindow = 10 * time.Minute

const maxBody = 1600 // ~10 concatenated SMS parts

// Notifier is told when a new outbound message is queued (the hub).
type Notifier interface{ Notify() }

type Server struct {
	Store   *store.Store
	Notify  Notifier
	Version string
}

// New builds an MCP server with all tools registered.
func (s *Server) New() *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "smsmcp", Version: s.Version}, &mcp.ServerOptions{
		Instructions: "Send and read SMS through the user's paired Android phone. " +
			"Always pass an idempotency_key to send_sms that stays the same if you retry the same send. " +
			"Use wait_for_reply to block until someone answers.",
	})
	mcp.AddTool(srv, &mcp.Tool{Name: "send_sms", Description: "Queue an SMS to be sent from the paired phone. " +
		"Safe to retry: calls with the same idempotency_key return the original message instead of sending twice."}, s.sendSMS)
	mcp.AddTool(srv, &mcp.Tool{Name: "get_message", Description: "Get one message and its delivery status."}, s.getMessage)
	mcp.AddTool(srv, &mcp.Tool{Name: "list_messages", Description: "List messages, newest first, filtered by number, direction or status."}, s.listMessages)
	mcp.AddTool(srv, &mcp.Tool{Name: "list_conversations", Description: "List conversation threads grouped by phone number, newest first."}, s.listConversations)
	mcp.AddTool(srv, &mcp.Tool{Name: "wait_for_reply", Description: "Wait until an SMS arrives from a number (e.g. a reply or an OTP), or until the timeout passes."}, s.waitForReply)
	mcp.AddTool(srv, &mcp.Tool{Name: "list_devices", Description: "List paired gateway phones with their number, status, battery and last-seen time."}, s.listDevices)
	return srv
}

func keyID(req *mcp.CallToolRequest) string {
	if req != nil && req.Extra != nil && req.Extra.TokenInfo != nil && req.Extra.TokenInfo.UserID != "" {
		return req.Extra.TokenInfo.UserID
	}
	return LocalKeyID
}

type SendIn struct {
	To             string `json:"to" jsonschema:"recipient phone number, international format preferred, e.g. +255712345678"`
	Body           string `json:"body" jsonschema:"message text; long texts are sent as a multipart SMS"`
	IdempotencyKey string `json:"idempotency_key,omitempty" jsonschema:"unique key for this logical send; reuse it on retries. If omitted, identical to+body within 10 minutes is treated as a retry"`
	AllowDuplicate bool   `json:"allow_duplicate,omitempty" jsonschema:"set true to deliberately send the same text again without an idempotency_key"`
	DeviceID       string `json:"device_id,omitempty" jsonschema:"gateway phone to send from; defaults to the most recently seen active phone"`
}

type SendOut struct {
	Message   store.Message `json:"message"`
	Duplicate bool          `json:"duplicate" jsonschema:"true if this call matched an earlier send and nothing new was queued"`
}

func (s *Server) sendSMS(ctx context.Context, req *mcp.CallToolRequest, in SendIn) (*mcp.CallToolResult, SendOut, error) {
	to, err := phone.Normalize(in.To)
	if err != nil {
		return nil, SendOut{}, err
	}
	body := strings.TrimSpace(in.Body)
	if body == "" {
		return nil, SendOut{}, errors.New("body is empty")
	}
	if utf8.RuneCountInString(body) > maxBody {
		return nil, SendOut{}, fmt.Errorf("body is longer than %d characters", maxBody)
	}
	key := in.IdempotencyKey
	switch {
	case key != "":
		key = "user:" + key
	case in.AllowDuplicate:
		key = "dup:" + ids.ULID(time.Now())
	default:
		bucket := time.Now().UnixMilli() / AutoKeyWindow.Milliseconds()
		key = "auto:" + ids.Hash(to, body, strconv.FormatInt(bucket, 10))
	}
	m, created, err := s.Store.CreateOutboundMessage(ctx, store.CreateOutbound{
		APIKeyID: keyID(req), IdempotencyKey: key, DeviceID: in.DeviceID, To: to, Body: body,
	})
	if err != nil {
		return nil, SendOut{}, err
	}
	if created && s.Notify != nil {
		s.Notify.Notify()
	}
	return nil, SendOut{Message: m, Duplicate: !created}, nil
}

type GetIn struct {
	MessageID string `json:"message_id" jsonschema:"id returned by send_sms or list_messages"`
}

func (s *Server) getMessage(ctx context.Context, _ *mcp.CallToolRequest, in GetIn) (*mcp.CallToolResult, store.Message, error) {
	m, err := s.Store.GetMessage(ctx, in.MessageID)
	return nil, m, err
}

type ListIn struct {
	Number    string `json:"number,omitempty" jsonschema:"only messages with this phone number"`
	Direction string `json:"direction,omitempty" jsonschema:"in or out"`
	Status    string `json:"status,omitempty" jsonschema:"e.g. queued, sent, delivered, failed, unknown, received"`
	Before    string `json:"before,omitempty" jsonschema:"RFC3339 time; only messages created before it (for paging)"`
	Limit     int    `json:"limit,omitempty" jsonschema:"max results, default 50, max 200"`
}

type ListOut struct {
	Messages []store.Message `json:"messages"`
}

func (s *Server) listMessages(ctx context.Context, _ *mcp.CallToolRequest, in ListIn) (*mcp.CallToolResult, ListOut, error) {
	f := store.ListFilter{Direction: in.Direction, Status: in.Status, Limit: in.Limit}
	if in.Number != "" {
		n, err := phone.Normalize(in.Number)
		if err != nil {
			return nil, ListOut{}, err
		}
		f.Peer = n
	}
	if in.Before != "" {
		t, err := time.Parse(time.RFC3339, in.Before)
		if err != nil {
			return nil, ListOut{}, fmt.Errorf("before: %w", err)
		}
		f.Before = t.UnixMilli()
	}
	msgs, err := s.Store.ListMessages(ctx, f)
	if msgs == nil {
		msgs = []store.Message{}
	}
	return nil, ListOut{Messages: msgs}, err
}

type ConvIn struct {
	Limit int `json:"limit,omitempty" jsonschema:"max threads, default 50"`
}

type ConvOut struct {
	Conversations []store.Conversation `json:"conversations"`
}

func (s *Server) listConversations(ctx context.Context, _ *mcp.CallToolRequest, in ConvIn) (*mcp.CallToolResult, ConvOut, error) {
	c, err := s.Store.ListConversations(ctx, in.Limit)
	if c == nil {
		c = []store.Conversation{}
	}
	return nil, ConvOut{Conversations: c}, err
}

type WaitIn struct {
	From           string `json:"from" jsonschema:"phone number to wait for"`
	Since          string `json:"since,omitempty" jsonschema:"RFC3339 time; only count messages after it. Defaults to now"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"how long to wait, default 120, max 600"`
}

type WaitOut struct {
	Message  *store.Message `json:"message,omitempty"`
	TimedOut bool           `json:"timed_out"`
}

func (s *Server) waitForReply(ctx context.Context, _ *mcp.CallToolRequest, in WaitIn) (*mcp.CallToolResult, WaitOut, error) {
	from, err := phone.Normalize(in.From)
	if err != nil {
		return nil, WaitOut{}, err
	}
	since := time.Now()
	if in.Since != "" {
		if since, err = time.Parse(time.RFC3339, in.Since); err != nil {
			return nil, WaitOut{}, fmt.Errorf("since: %w", err)
		}
	}
	timeout := time.Duration(in.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	timeout = min(timeout, 600*time.Second)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		// Inbound numbers may be formatted differently from what the agent
		// passed (+255... vs 0...), so match loosely on recent inbound.
		msgs, err := s.Store.ListMessages(ctx, store.ListFilter{Direction: "in", After: since.UnixMilli(), Limit: 200})
		if err != nil && ctx.Err() == nil {
			return nil, WaitOut{}, err
		}
		for i := len(msgs) - 1; i >= 0; i-- { // oldest first
			if phone.SameNumber(msgs[i].PeerNumber, from) {
				m := msgs[i]
				return nil, WaitOut{Message: &m}, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, WaitOut{TimedOut: true}, nil
		case <-t.C:
		}
	}
}

type DevicesOut struct {
	Devices []store.Device `json:"devices"`
}

func (s *Server) listDevices(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, DevicesOut, error) {
	d, err := s.Store.ListDevices(ctx)
	if d == nil {
		d = []store.Device{}
	}
	return nil, DevicesOut{Devices: d}, err
}
