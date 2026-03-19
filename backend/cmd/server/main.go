package main

import (
	"log/slog"
	"net"
	"net/http"
	"os"

	"google.golang.org/grpc"

	pb "github.com/PhilstarHosiery/stargate/backend/gen"
	"github.com/PhilstarHosiery/stargate/backend/config"
	"github.com/PhilstarHosiery/stargate/backend/internal/db"
	grpcserver "github.com/PhilstarHosiery/stargate/backend/internal/grpc"
	"github.com/PhilstarHosiery/stargate/backend/internal/sms"
)

func main() {
	// Load config — try config/config.yaml first, fall back to config.yaml.
	cfg, err := loadConfig()
	if err != nil {
		slog.Error("failed to load config", "err", err)
		os.Exit(1)
	}

	// Open and migrate the database.
	database, err := db.Open(cfg.Database.Path)
	if err != nil {
		slog.Error("failed to open database", "err", err, "path", cfg.Database.Path)
		os.Exit(1)
	}
	defer database.Close()

	if err := db.Migrate(database); err != nil {
		slog.Error("failed to migrate database", "err", err)
		os.Exit(1)
	}
	slog.Info("database ready", "path", cfg.Database.Path)

	// Create application components.
	smsOutbound := sms.NewOutboundClient(cfg.SMS.GateURL, cfg.SMS.APIKey)
	streamMgr := grpcserver.NewStreamManager()
	server := grpcserver.NewServer(database, streamMgr, smsOutbound)

	// Set up gRPC server.
	grpcSrv := grpc.NewServer()
	pb.RegisterStarGateCoreServer(grpcSrv, server)

	// Start webhook HTTP server in background.
	webhookHandler := sms.NewWebhookHandler(database, streamMgr)
	mux := http.NewServeMux()
	mux.HandleFunc("/webhook", webhookHandler)

	go func() {
		slog.Info("webhook server listening", "addr", cfg.Server.WebhookAddr)
		if err := http.ListenAndServe(cfg.Server.WebhookAddr, mux); err != nil {
			slog.Error("webhook server failed", "err", err)
			os.Exit(1)
		}
	}()

	// Start gRPC server (blocking).
	lis, err := net.Listen("tcp", cfg.Server.GRPCAddr)
	if err != nil {
		slog.Error("failed to listen on gRPC address", "err", err, "addr", cfg.Server.GRPCAddr)
		os.Exit(1)
	}
	slog.Info("gRPC server listening", "addr", cfg.Server.GRPCAddr)
	if err := grpcSrv.Serve(lis); err != nil {
		slog.Error("gRPC server failed", "err", err)
		os.Exit(1)
	}
}

// loadConfig tries config/config.yaml then config.yaml.
func loadConfig() (*config.Config, error) {
	paths := []string{"config/config.yaml", "config.yaml"}
	var lastErr error
	for _, p := range paths {
		cfg, err := config.Load(p)
		if err == nil {
			slog.Info("loaded config", "path", p)
			return cfg, nil
		}
		lastErr = err
	}
	return nil, lastErr
}
