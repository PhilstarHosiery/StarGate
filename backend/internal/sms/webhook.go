package sms

import (
	"encoding/json"
	"log/slog"
	"net/http"

	pb "github.com/PhilstarHosiery/stargate/backend/gen"
	"github.com/PhilstarHosiery/stargate/backend/internal/db"
)

// Broadcaster is the subset of StreamManager used by the webhook handler.
type Broadcaster interface {
	Broadcast(event *pb.MessageEvent, userIDs []string)
}

// inboundPayload is the JSON body from SMS Gate / RUT241.
type inboundPayload struct {
	From    string `json:"from"`
	Message string `json:"message"`
	Sim     int    `json:"sim"`
}

// NewWebhookHandler returns an http.HandlerFunc that processes inbound SMS webhooks.
func NewWebhookHandler(database *db.DB, broadcaster Broadcaster) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var payload inboundPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			slog.Warn("webhook: bad JSON body", "err", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if payload.From == "" || payload.Message == "" {
			http.Error(w, "missing 'from' or 'message'", http.StatusBadRequest)
			return
		}

		slog.Info("webhook: inbound SMS", "from", payload.From, "sim", payload.Sim)

		// Look up or create the contact.
		contact, err := database.GetContactByPhone(payload.From)
		if err != nil {
			slog.Error("webhook: GetContactByPhone failed", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if contact == nil {
			contact, err = database.CreateContact(payload.From, payload.Sim)
			if err != nil {
				slog.Error("webhook: CreateContact failed", "err", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			slog.Info("webhook: created unknown contact", "phone", payload.From)
		}

		// Look up or create an open session.
		sess, err := database.GetOpenSessionByPhone(payload.From)
		if err != nil {
			slog.Error("webhook: GetOpenSessionByPhone failed", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if sess == nil {
			sess, err = database.CreateSession(payload.From)
			if err != nil {
				slog.Error("webhook: CreateSession failed", "err", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			slog.Info("webhook: created new session", "session_id", sess.SessionID)
		}

		// Store the inbound message.
		if _, err := database.CreateMessage(sess.SessionID, "INBOUND", payload.Message, ""); err != nil {
			slog.Error("webhook: CreateMessage failed", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
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
				MessageText: payload.Message,
				SenderType:  "Contact",
			}, targetUsers)
		}

		w.WriteHeader(http.StatusOK)
	}
}
