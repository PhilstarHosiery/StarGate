package sms

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"encoding/json"

	pb "github.com/PhilstarHosiery/stargate/backend/gen"
	"github.com/PhilstarHosiery/stargate/backend/internal/db"
)

// Broadcaster is the subset of StreamManager used by the webhook handler.
type Broadcaster interface {
	Broadcast(event *pb.MessageEvent, userIDs []string)
}

// smsGateEvent is the top-level JSON body sent by SMS Gate on inbound SMS.
type smsGateEvent struct {
	Event   string         `json:"event"`
	Payload smsGatePayload `json:"payload"`
}

type smsGatePayload struct {
	MessageID string `json:"messageId"`
	Sender    string `json:"sender"`
	Message   string `json:"message"`
	SimNumber int    `json:"simNumber"`
}

// NewWebhookHandler returns an http.HandlerFunc that processes inbound SMS webhooks.
// webhookSecret is the HMAC-SHA256 signing key configured in SMS Gate → Settings → Webhooks.
// Pass an empty string to skip signature verification (not recommended in production).
func NewWebhookHandler(database *db.DB, broadcaster Broadcaster, webhookSecret string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Read the body once so we can verify the HMAC and then decode.
		body, err := io.ReadAll(r.Body)
		if err != nil {
			slog.Warn("webhook: failed to read body", "err", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		// Verify HMAC-SHA256 signature if a secret is configured.
		if webhookSecret != "" {
			if !verifySignature(webhookSecret, body, r.Header) {
				slog.Warn("webhook: signature verification failed")
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}

		var event smsGateEvent
		if err := json.NewDecoder(bytes.NewReader(body)).Decode(&event); err != nil {
			slog.Warn("webhook: bad JSON body", "err", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		// Only handle inbound SMS events.
		if event.Event != "sms:received" {
			w.WriteHeader(http.StatusOK)
			return
		}

		phone := event.Payload.Sender
		message := event.Payload.Message
		sim := event.Payload.SimNumber

		if phone == "" || message == "" {
			http.Error(w, "missing sender or message", http.StatusBadRequest)
			return
		}

		slog.Info("webhook: inbound SMS", "from", phone, "sim", sim)

		// Look up or create the contact.
		contact, err := database.GetContactByPhone(phone)
		if err != nil {
			slog.Error("webhook: GetContactByPhone failed", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if contact == nil {
			contact, err = database.CreateContact(phone, sim)
			if err != nil {
				slog.Error("webhook: CreateContact failed", "err", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			slog.Info("webhook: created unknown contact", "phone", phone)
		}

		// Look up or create an open session.
		sess, err := database.GetOpenSessionByPhone(phone)
		if err != nil {
			slog.Error("webhook: GetOpenSessionByPhone failed", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if sess == nil {
			sess, err = database.CreateSession(phone)
			if err != nil {
				slog.Error("webhook: CreateSession failed", "err", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			slog.Info("webhook: created new session", "session_id", sess.SessionID)
		}

		// Store the inbound message; nil return means duplicate — already processed.
		msg, err := database.CreateMessage(sess.SessionID, "INBOUND", message, "", event.Payload.MessageID)
		if err != nil {
			slog.Error("webhook: CreateMessage failed", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if msg == nil {
			slog.Info("webhook: duplicate message ignored", "gateway_id", event.Payload.MessageID)
			w.WriteHeader(http.StatusOK)
			return
		}

		// Determine which users to notify.
		groupID := ""
		if contact.GroupID.Valid {
			groupID = contact.GroupID.String
		}
		targetUsers, err := database.GetUsersWithAccessToGroup(groupID)
		if err != nil {
			slog.Error("webhook: GetUsersWithAccessToGroup failed", "err", err)
			// Don't fail the request — just log.
		} else {
			broadcaster.Broadcast(&pb.MessageEvent{
				SessionId:   sess.SessionID,
				MessageText: message,
				SenderType:  "Contact",
			}, targetUsers)
		}

		w.WriteHeader(http.StatusOK)
	}
}

// verifySignature checks the X-Signature header against HMAC-SHA256(secret, body).
// It also rejects requests whose X-Timestamp is more than 5 minutes old.
func verifySignature(secret string, body []byte, headers http.Header) bool {
	tsStr := headers.Get("X-Timestamp")
	sig := headers.Get("X-Signature")
	if tsStr == "" || sig == "" {
		return false
	}

	// Reject stale requests.
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return false
	}
	age := time.Since(time.Unix(ts, 0)).Abs()
	if age > 5*time.Minute {
		slog.Warn("webhook: stale timestamp rejected", "age", age)
		return false
	}

	// SMS Gate signs: HMAC-SHA256(secret, body + timestamp)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	mac.Write([]byte(tsStr))
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(sig))
}
