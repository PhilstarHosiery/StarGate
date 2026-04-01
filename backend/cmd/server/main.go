package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"
	"google.golang.org/grpc"

	pb "github.com/PhilstarHosiery/stargate/backend/gen"
	"github.com/PhilstarHosiery/stargate/backend/config"
	"github.com/PhilstarHosiery/stargate/backend/internal/db"
	grpcserver "github.com/PhilstarHosiery/stargate/backend/internal/grpc"
	"github.com/PhilstarHosiery/stargate/backend/internal/sms"
)

var (
	logFileMu      sync.Mutex
	currentLogFile *os.File
)

func main() {
	createUser := flag.Bool("create-user", false, "interactively create a new user and exit")
	configPath := flag.String("config", "", "path to config YAML file (default: config/config.yaml, then config.yaml)")
	logPath := flag.String("log", "", "path to log file; send SIGHUP to reopen after rotation (default: stdout)")
	pidFile := flag.String("pid-file", "", "write process PID to this file on startup")
	flag.Parse()

	if *logPath != "" {
		if err := openLogFile(*logPath); err != nil {
			fmt.Fprintf(os.Stderr, "failed to open log file: %v\n", err)
			os.Exit(1)
		}
	}

	if *pidFile != "" {
		if err := os.WriteFile(*pidFile, fmt.Appendf(nil, "%d\n", os.Getpid()), 0644); err != nil {
			slog.Error("failed to write pid file", "err", err)
			os.Exit(1)
		}
		defer os.Remove(*pidFile)
	}

	cfg, err := loadConfig(*configPath)
	if err != nil {
		slog.Error("failed to load config", "err", err)
		os.Exit(1)
	}

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

	smsOutbound := sms.NewOutboundClient(cfg.SMS.GateURL, cfg.SMS.Username, cfg.SMS.Password, cfg.SMS.APIKey)
	streamMgr := grpcserver.NewStreamManager()
	server := grpcserver.NewServer(database, streamMgr, smsOutbound)

	grpcSrv := grpc.NewServer()
	pb.RegisterStarGateCoreServer(grpcSrv, server)

	// HTTP server: webhook + health check.
	webhookHandler := sms.NewWebhookHandler(database, streamMgr, cfg.SMS.WebhookSecret)
	mux := http.NewServeMux()
	mux.HandleFunc("/webhook", webhookHandler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	httpSrv := &http.Server{Addr: cfg.Server.WebhookAddr, Handler: mux}

	go func() {
		slog.Info("webhook server listening", "addr", cfg.Server.WebhookAddr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("webhook server failed", "err", err)
			os.Exit(1)
		}
	}()

	if cfg.SMS.WebhookURL != "" {
		if err := smsOutbound.RegisterWebhook(cfg.SMS.WebhookURL); err != nil {
			slog.Error("failed to register webhook with SMS Gate", "err", err)
			os.Exit(1)
		}
		slog.Info("webhook registered with SMS Gate", "url", cfg.SMS.WebhookURL)
	}

	lis, err := net.Listen("tcp", cfg.Server.GRPCAddr)
	if err != nil {
		slog.Error("failed to listen on gRPC address", "err", err, "addr", cfg.Server.GRPCAddr)
		os.Exit(1)
	}
	slog.Info("gRPC server listening", "addr", cfg.Server.GRPCAddr)

	go func() {
		if err := grpcSrv.Serve(lis); err != nil {
			slog.Error("gRPC server failed", "err", err)
			os.Exit(1)
		}
	}()

	// Block until SIGINT/SIGTERM; reopen log file on SIGHUP.
	quit := make(chan os.Signal, 1)
	hup := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	signal.Notify(hup, syscall.SIGHUP)

signals:
	for {
		select {
		case sig := <-quit:
			slog.Info("shutting down", "signal", sig)
			break signals
		case <-hup:
			if *logPath != "" {
				if err := openLogFile(*logPath); err != nil {
					slog.Error("failed to reopen log file", "err", err)
				} else {
					slog.Info("log file reopened")
				}
			}
		}
	}

	grpcSrv.GracefulStop()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(ctx); err != nil {
		slog.Error("http shutdown error", "err", err)
	}

	slog.Info("shutdown complete")
}

// openLogFile opens path for appending, sets it as the slog destination, and
// closes the previously open log file (if any). Safe to call on SIGHUP.
func openLogFile(path string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0640)
	if err != nil {
		return err
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(f, nil)))

	logFileMu.Lock()
	old := currentLogFile
	currentLogFile = f
	logFileMu.Unlock()

	if old != nil {
		old.Close()
	}
	return nil
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

	if _, err := database.CreateUser(username, string(hash), globalAccess); err != nil {
		fmt.Fprintf(os.Stderr, "error creating user: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("User %q created (global_access=%v)\n", username, globalAccess)
}

// loadConfig tries the explicit path first, then falls back to config/config.yaml and config.yaml.
func loadConfig(path string) (*config.Config, error) {
	if path != "" {
		cfg, err := config.Load(path)
		if err != nil {
			return nil, err
		}
		slog.Info("loaded config", "path", path)
		return cfg, nil
	}
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
