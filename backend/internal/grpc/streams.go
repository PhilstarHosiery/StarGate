package grpc

import (
	"log/slog"
	"sync"

	pb "github.com/PhilstarHosiery/stargate/backend/gen"
)

// StreamManager tracks active gRPC SubscribeToInbox streams keyed by user ID.
type StreamManager struct {
	mu      sync.RWMutex
	streams map[string]pb.StarGateCore_SubscribeToInboxServer
}

// NewStreamManager creates an empty StreamManager.
func NewStreamManager() *StreamManager {
	return &StreamManager{
		streams: make(map[string]pb.StarGateCore_SubscribeToInboxServer),
	}
}

// Register stores a stream for the given user. Any previous stream for that
// user is silently replaced.
func (sm *StreamManager) Register(userID string, stream pb.StarGateCore_SubscribeToInboxServer) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.streams[userID] = stream
	slog.Info("stream registered", "user_id", userID)
}

// Unregister removes the stream for the given user.
func (sm *StreamManager) Unregister(userID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	delete(sm.streams, userID)
	slog.Info("stream unregistered", "user_id", userID)
}

// Broadcast sends event to every user listed in userIDs that has an active
// stream. Errors on individual sends are logged but do not stop delivery to
// other users.
func (sm *StreamManager) Broadcast(event *pb.MessageEvent, userIDs []string) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	for _, uid := range userIDs {
		stream, ok := sm.streams[uid]
		if !ok {
			continue
		}
		if err := stream.Send(event); err != nil {
			slog.Warn("broadcast send failed", "user_id", uid, "err", err)
		}
	}
}
