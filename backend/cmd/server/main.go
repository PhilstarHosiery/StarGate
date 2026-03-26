package main

import (
	"bufio"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"
	"google.golang.org/grpc"

	pb "github.com/PhilstarHosiery/stargate/backend/gen"
	"github.com/PhilstarHosiery/stargate/backend/config"
	"github.com/PhilstarHosiery/stargate/backend/internal/db"
	grpcserver "github.com/PhilstarHosiery/stargate/backend/internal/grpc"
	"github.com/PhilstarHosiery/stargate/backend/internal/sms"
)

func main() {
	createUser := flag.Bool("create-user", false, "interactively create a new user and exit")
	flag.Parse()

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

	if *createUser {
		runCreateUser(database)
		return
	}

	// Create application components.
	smsOutbound := sms.NewOutboundClient(cfg.SMS.GateURL, cfg.SMS.Username, cfg.SMS.Password, cfg.SMS.APIKey)
	streamMgr := grpcserver.NewStreamManager()
	server := grpcserver.NewServer(database, streamMgr, smsOutbound)

	// Set up gRPC server.
	grpcSrv := grpc.NewServer()
	pb.RegisterStarGateCoreServer(grpcSrv, server)

	// Start webhook HTTP server in background.
	webhookHandler := sms.NewWebhookHandler(database, streamMgr, cfg.SMS.WebhookSecret)
	mux := http.NewServeMux()
	mux.HandleFunc("/webhook", webhookHandler)

	go func() {
		slog.Info("webhook server listening", "addr", cfg.Server.WebhookAddr)
		if err := http.ListenAndServe(cfg.Server.WebhookAddr, mux); err != nil {
			slog.Error("webhook server failed", "err", err)
			os.Exit(1)
		}
	}()

	// Register this server's webhook URL with the SMS Gate app.
	if cfg.SMS.WebhookURL != "" {
		if err := smsOutbound.RegisterWebhook(cfg.SMS.WebhookURL); err != nil {
			slog.Error("failed to register webhook with SMS Gate", "err", err)
			os.Exit(1)
		}
		slog.Info("webhook registered with SMS Gate", "url", cfg.SMS.WebhookURL)
	}

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

// runCreateUser prompts for credentials, creates a user, and exits.
func runCreateUser(database *db.DB) {
	reader := bufio.NewReader(os.Stdin)

	fmt.Print("Username: ")
	username, _ := reader.ReadString('\n')
	username = strings.TrimSpace(username)
	if username == "" {
		fmt.Fprintln(os.Stderr, "error: username cannot be empty")
		os.Exit(1)
	}

	fmt.Print("Password: ")
	passwordBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading password: %v\n", err)
		os.Exit(1)
	}
	if len(passwordBytes) == 0 {
		fmt.Fprintln(os.Stderr, "error: password cannot be empty")
		os.Exit(1)
	}

	fmt.Print("Global access (HR — sees all groups)? [y/N]: ")
	answer, _ := reader.ReadString('\n')
	globalAccess := strings.ToLower(strings.TrimSpace(answer)) == "y"

	hash, err := bcrypt.GenerateFromPassword(passwordBytes, bcrypt.DefaultCost)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error hashing password: %v\n", err)
		os.Exit(1)
	}

	if err := database.CreateUser(username, string(hash), globalAccess); err != nil {
		fmt.Fprintf(os.Stderr, "error creating user: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("User %q created (global_access=%v)\n", username, globalAccess)
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
