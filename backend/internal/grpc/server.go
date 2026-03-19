package grpc

import (
	"context"
	"fmt"
	"log/slog"

	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/PhilstarHosiery/stargate/backend/gen"
	"github.com/PhilstarHosiery/stargate/backend/internal/db"
	"github.com/PhilstarHosiery/stargate/backend/internal/sms"
)

// Server implements pb.StarGateCoreServer.
type Server struct {
	pb.UnimplementedStarGateCoreServer

	db      *db.DB
	streams *StreamManager
	smsOut  *sms.OutboundClient
}

// NewServer creates a new gRPC server with the given dependencies.
func NewServer(database *db.DB, streams *StreamManager, smsOut *sms.OutboundClient) *Server {
	return &Server{
		db:      database,
		streams: streams,
		smsOut:  smsOut,
	}
}

// Login validates username/password and returns the user's ID on success.
func (s *Server) Login(ctx context.Context, req *pb.LoginRequest) (*pb.LoginResponse, error) {
	if req.Username == "" || req.Password == "" {
		return &pb.LoginResponse{Success: false, ErrorMessage: "username and password are required"}, nil
	}

	user, err := s.db.GetUserByUsername(req.Username)
	if err != nil {
		slog.Error("Login: db error", "err", err)
		return nil, status.Error(codes.Internal, "internal error")
	}
	if user == nil {
		return &pb.LoginResponse{Success: false, ErrorMessage: "invalid credentials"}, nil
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		return &pb.LoginResponse{Success: false, ErrorMessage: "invalid credentials"}, nil
	}

	slog.Info("Login: success", "user_id", user.UserID, "username", user.Username)
	return &pb.LoginResponse{Success: true, UserId: user.UserID}, nil
}

// GetSessions returns all sessions the user has permission to see.
func (s *Server) GetSessions(ctx context.Context, req *pb.User) (*pb.SessionsResponse, error) {
	sessions, err := s.db.GetSessionsByUserAccess(req.UserId)
	if err != nil {
		slog.Error("GetSessions: db error", "err", err, "user_id", req.UserId)
		return nil, status.Error(codes.Internal, "internal error")
	}

	var pbSessions []*pb.ChatSession
	for _, sess := range sessions {
		// Resolve the contact's group for this session.
		contact, err := s.db.GetContactByPhone(sess.ContactPhone)
		if err != nil {
			slog.Warn("GetSessions: contact lookup failed", "phone", sess.ContactPhone, "err", err)
		}
		groupID := ""
		if contact != nil && contact.GroupID.Valid {
			groupID = contact.GroupID.String
		}
		pbSessions = append(pbSessions, &pb.ChatSession{
			SessionId:    sess.SessionID,
			ContactPhone: sess.ContactPhone,
			GroupId:      groupID,
			Status:       sess.Status,
		})
	}

	return &pb.SessionsResponse{Sessions: pbSessions}, nil
}

// GetSession returns a single session by ID.
func (s *Server) GetSession(ctx context.Context, req *pb.SessionRequest) (*pb.ChatSession, error) {
	sess, err := s.db.GetSessionByID(req.SessionId)
	if err != nil {
		return nil, status.Error(codes.Internal, "internal error")
	}
	if sess == nil {
		return nil, status.Errorf(codes.NotFound, "session %q not found", req.SessionId)
	}

	contact, err := s.db.GetContactByPhone(sess.ContactPhone)
	if err != nil {
		slog.Warn("GetSession: contact lookup failed", "phone", sess.ContactPhone, "err", err)
	}
	groupID := ""
	if contact != nil && contact.GroupID.Valid {
		groupID = contact.GroupID.String
	}

	return &pb.ChatSession{
		SessionId:    sess.SessionID,
		ContactPhone: sess.ContactPhone,
		GroupId:      groupID,
		Status:       sess.Status,
	}, nil
}

// GetMessages returns all messages for a session.
func (s *Server) GetMessages(ctx context.Context, req *pb.SessionRequest) (*pb.MessagesResponse, error) {
	msgs, err := s.db.GetMessagesBySession(req.SessionId)
	if err != nil {
		return nil, status.Error(codes.Internal, "internal error")
	}

	var pbMsgs []*pb.Message
	for _, m := range msgs {
		sentBy := ""
		if m.SentByUserID.Valid {
			sentBy = m.SentByUserID.String
		}
		pbMsgs = append(pbMsgs, &pb.Message{
			MessageId:    m.MessageID,
			SessionId:    m.SessionID,
			Direction:    m.Direction,
			Text:         m.Text,
			SentByUserId: sentBy,
			Timestamp:    m.Timestamp.Format("2006-01-02T15:04:05Z"),
		})
	}

	return &pb.MessagesResponse{Messages: pbMsgs}, nil
}

// SubscribeToInbox registers the caller's stream and blocks until the client
// disconnects (context cancellation).
func (s *Server) SubscribeToInbox(req *pb.User, stream pb.StarGateCore_SubscribeToInboxServer) error {
	if req.UserId == "" {
		return status.Error(codes.InvalidArgument, "user_id is required")
	}

	s.streams.Register(req.UserId, stream)
	defer s.streams.Unregister(req.UserId)

	// Block until the client disconnects.
	<-stream.Context().Done()
	slog.Info("SubscribeToInbox: client disconnected", "user_id", req.UserId)
	return nil
}

// SendReply stores an outbound message and dispatches it via the SMS gateway.
func (s *Server) SendReply(ctx context.Context, req *pb.ReplyRequest) (*pb.ActionResponse, error) {
	if req.SessionId == "" || req.MessageText == "" || req.UserId == "" {
		return &pb.ActionResponse{Success: false, ErrorMessage: "session_id, message_text and user_id are required"}, nil
	}

	sess, err := s.db.GetSessionByID(req.SessionId)
	if err != nil {
		return nil, status.Error(codes.Internal, "internal error")
	}
	if sess == nil {
		return &pb.ActionResponse{Success: false, ErrorMessage: fmt.Sprintf("session %q not found", req.SessionId)}, nil
	}

	contact, err := s.db.GetContactByPhone(sess.ContactPhone)
	if err != nil || contact == nil {
		return &pb.ActionResponse{Success: false, ErrorMessage: "contact not found"}, nil
	}

	// Store the outbound message.
	if _, err := s.db.CreateMessage(req.SessionId, "OUTBOUND", req.MessageText, req.UserId); err != nil {
		slog.Error("SendReply: CreateMessage failed", "err", err)
		return nil, status.Error(codes.Internal, "internal error")
	}

	// Dispatch via SMS gateway using the contact's assigned SIM.
	if err := s.smsOut.Send(sess.ContactPhone, contact.AssignedSim, req.MessageText); err != nil {
		slog.Error("SendReply: SMS dispatch failed", "err", err, "phone", sess.ContactPhone)
		return &pb.ActionResponse{Success: false, ErrorMessage: "SMS dispatch failed: " + err.Error()}, nil
	}

	// Broadcast to other connected users who have access to this session's group.
	groupID := ""
	if contact.GroupID.Valid {
		groupID = contact.GroupID.String
	}
	targetUsers, err := s.db.GetUsersWithAccessToGroup(groupID)
	if err != nil {
		slog.Warn("SendReply: GetUsersWithAccessToGroup failed", "err", err)
	} else {
		// Exclude the sender from the broadcast.
		var others []string
		for _, uid := range targetUsers {
			if uid != req.UserId {
				others = append(others, uid)
			}
		}
		s.streams.Broadcast(&pb.MessageEvent{
			SessionId:   req.SessionId,
			MessageText: req.MessageText,
			SenderType:  req.UserId,
		}, others)
	}

	return &pb.ActionResponse{Success: true}, nil
}

// RenameContact updates a contact's display name.
func (s *Server) RenameContact(ctx context.Context, req *pb.RenameRequest) (*pb.ActionResponse, error) {
	if req.ContactPhone == "" || req.Name == "" {
		return &pb.ActionResponse{Success: false, ErrorMessage: "contact_phone and name are required"}, nil
	}

	if err := s.db.RenameContact(req.ContactPhone, req.Name, req.UserId); err != nil {
		slog.Error("RenameContact: db error", "err", err)
		return nil, status.Error(codes.Internal, "internal error")
	}

	return &pb.ActionResponse{Success: true}, nil
}

// AssignContact updates a contact's group and broadcasts the re-routing event
// to users who now have permission to see the contact's messages.
func (s *Server) AssignContact(ctx context.Context, req *pb.AssignRequest) (*pb.ActionResponse, error) {
	if req.ContactPhone == "" || req.GroupId == "" {
		return &pb.ActionResponse{Success: false, ErrorMessage: "contact_phone and group_id are required"}, nil
	}

	if err := s.db.AssignContact(req.ContactPhone, req.GroupId, req.UserId); err != nil {
		slog.Error("AssignContact: db error", "err", err)
		return nil, status.Error(codes.Internal, "internal error")
	}

	// Find the open session for this contact to broadcast the re-routing event.
	sess, err := s.db.GetOpenSessionByPhone(req.ContactPhone)
	if err != nil {
		slog.Warn("AssignContact: GetOpenSessionByPhone failed", "err", err)
	}
	if sess != nil {
		targetUsers, err := s.db.GetUsersWithAccessToGroup(req.GroupId)
		if err != nil {
			slog.Warn("AssignContact: GetUsersWithAccessToGroup failed", "err", err)
		} else {
			s.streams.Broadcast(&pb.MessageEvent{
				SessionId:   sess.SessionID,
				MessageText: "",
				SenderType:  "system",
			}, targetUsers)
		}
	}

	return &pb.ActionResponse{Success: true}, nil
}

// RetireContact retires the old contact record and returns a fresh session for
// the same phone number.
func (s *Server) RetireContact(ctx context.Context, req *pb.RetireRequest) (*pb.ChatSession, error) {
	if req.ContactPhone == "" {
		return nil, status.Error(codes.InvalidArgument, "contact_phone is required")
	}

	newSession, err := s.db.RetireContact(req.ContactPhone, req.UserId)
	if err != nil {
		slog.Error("RetireContact: db error", "err", err)
		return nil, status.Error(codes.Internal, "internal error")
	}

	return &pb.ChatSession{
		SessionId:    newSession.SessionID,
		ContactPhone: newSession.ContactPhone,
		GroupId:      "",
		Status:       newSession.Status,
	}, nil
}
