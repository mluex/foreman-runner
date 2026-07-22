package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/mluex/foreman-runner/internal/api"
	"github.com/mluex/foreman-runner/internal/config"
	"github.com/mluex/foreman-runner/internal/platform"
)

// cmdEnroll registers this machine with a foreman server: it detects the
// platform, generates an Ed25519 keypair, exchanges the enrollment code for an
// API token, and writes the runner config.
func cmdEnroll(args []string) error {
	fs := flag.NewFlagSet("enroll", flag.ExitOnError)
	var (
		code       = fs.String("code", "", "enrollment code from the foreman web UI (required)")
		server     = fs.String("server", "", "foreman server base URL, e.g. https://foreman.example.com (required)")
		name       = fs.String("name", "", "runner name hint (default: hostname)")
		configPath = fs.String("config", config.DefaultPath(), "path to the runner config file")
		insecure   = fs.Bool("insecure", false, "skip TLS certificate verification (dev/self-signed only)")
		force      = fs.Bool("force", false, "overwrite an existing runner config")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *code == "" {
		return fmt.Errorf("--code is required")
	}
	if *server == "" {
		return fmt.Errorf("--server is required")
	}
	if config.Exists(*configPath) && !*force {
		return fmt.Errorf("already enrolled (%s); pass --force to re-enroll", *configPath)
	}

	goos, arch, err := platform.Detect()
	if err != nil {
		return err
	}

	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "unknown-host"
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate keypair: %w", err)
	}

	client := api.New(*server, *insecure)
	resp, err := client.Enroll(api.EnrollRequest{
		Code:         *code,
		RunnerPubKey: base64.StdEncoding.EncodeToString(pub),
		Hostname:     hostname,
		OS:           goos,
		Arch:         arch,
		NameHint:     *name,
	})
	if err != nil {
		return err
	}

	cfg := &config.Config{
		RunnerID:      resp.RunnerID,
		RunnerPrivKey: base64.StdEncoding.EncodeToString(priv),
		APIToken:      resp.APIToken,
		UserPubKey:    resp.UserPubKey,
		ServerURL:     strings.TrimRight(*server, "/"),
		OS:            goos,
		Arch:          arch,
	}
	if err := config.Save(*configPath, cfg); err != nil {
		return err
	}

	fmt.Printf("enrolled  %s\n", resp.RunnerID)
	fmt.Printf("host      %s (%s/%s)\n", hostname, goos, arch)
	fmt.Printf("server    %s\n", cfg.ServerURL)
	fmt.Printf("config    %s\n", *configPath)
	fmt.Printf("\nthis machine is now registered.\n")
	fmt.Printf("install it as a background service:  ./foreman-runner install\n")
	fmt.Printf("or run it in the foreground:         ./foreman-runner run\n")
	return nil
}
