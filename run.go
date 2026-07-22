package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mluex/foreman-runner/internal/agent"
	"github.com/mluex/foreman-runner/internal/api"
	"github.com/mluex/foreman-runner/internal/config"
	"github.com/mluex/foreman-runner/internal/system"
)

// cmdRun is the runner daemon: it loads the enrolled config and sends signed
// heartbeats (with detected agents and host metrics) on an interval. Task
// polling and execution arrive in a later milestone.
func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	var (
		configPath = fs.String("config", config.DefaultPath(), "path to the runner config file")
		insecure   = fs.Bool("insecure", false, "skip TLS certificate verification (dev/self-signed only)")
		interval   = fs.Duration("interval", 30*time.Second, "heartbeat interval")
		once       = fs.Bool("once", false, "send a single heartbeat and exit")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("not enrolled? run \"foreman-runner enroll\" first: %w", err)
	}

	privBytes, err := base64.StdEncoding.DecodeString(cfg.RunnerPrivKey)
	if err != nil {
		return fmt.Errorf("decode runner private key: %w", err)
	}
	if len(privBytes) != ed25519.PrivateKeySize {
		return fmt.Errorf("invalid runner private key length: got %d, want %d", len(privBytes), ed25519.PrivateKeySize)
	}
	privKey := ed25519.PrivateKey(privBytes)

	client := api.New(cfg.ServerURL, *insecure)

	send := func() error {
		nonce := make([]byte, 16)
		if _, err := rand.Read(nonce); err != nil {
			return fmt.Errorf("generate nonce: %w", err)
		}

		req := api.HeartbeatRequest{
			RunnerID:  cfg.RunnerID,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Nonce:     base64.StdEncoding.EncodeToString(nonce),
			Agents:    agent.Detect(),
			System:    system.Sample(),
		}

		resp, err := client.Heartbeat(cfg.APIToken, privKey, req)
		if err != nil {
			return err
		}

		// pick up owner key rotation
		if resp.UserPubKey != "" && resp.UserPubKey != cfg.UserPubKey {
			cfg.UserPubKey = resp.UserPubKey
			if err := config.Save(*configPath, cfg); err != nil {
				fmt.Fprintln(os.Stderr, "warn: could not persist rotated user key:", err)
			}
		}

		fmt.Printf("heartbeat ok  agents=%d  server_time=%s\n", len(req.Agents), resp.ServerTime)
		return nil
	}

	if *once {
		return send()
	}

	fmt.Printf("runner %s heartbeating to %s every %s\n", cfg.RunnerID, cfg.ServerURL, *interval)
	if err := send(); err != nil {
		fmt.Fprintln(os.Stderr, "heartbeat error:", err)
	}

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	for {
		select {
		case <-ticker.C:
			if err := send(); err != nil {
				fmt.Fprintln(os.Stderr, "heartbeat error:", err)
			}
		case <-stop:
			fmt.Println("shutting down")
			return nil
		}
	}
}
