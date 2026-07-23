package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/mluex/foreman-runner/internal/api"
	"github.com/mluex/foreman-runner/internal/config"
	"github.com/mluex/foreman-runner/internal/service"
)

// cmdUpdate updates the runner in place. It asks the server for the latest
// released version, downloads the matching binary and checksums through the
// server's /dl proxy, verifies the SHA-256, atomically replaces the running
// binary, and restarts the background service so the new version takes over.
func cmdUpdate(args []string) error {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	var (
		configPath = fs.String("config", config.DefaultPath(), "path to the runner config file")
		server     = fs.String("server", "", "override the server URL from the config")
		insecure   = fs.Bool("insecure", false, "skip TLS certificate verification (dev/self-signed only)")
		force      = fs.Bool("force", false, "reinstall even if already on the latest version")
		noRestart  = fs.Bool("no-restart", false, "do not restart the service after updating")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("not enrolled? run \"foreman-runner enroll\" first: %w", err)
	}

	serverURL := cfg.ServerURL
	if *server != "" {
		serverURL = *server
	}
	client := api.New(serverURL, *insecure)

	latest, err := client.LatestVersion(cfg.APIToken)
	if err != nil {
		return fmt.Errorf("check latest version: %w", err)
	}

	fmt.Printf("current   %s\n", version)
	fmt.Printf("latest    %s\n", latest)
	if latest == version && !*force {
		fmt.Println("already up to date")

		return nil
	}

	asset := fmt.Sprintf("foreman-runner-%s-%s", runtime.GOOS, runtime.GOARCH)

	wantSum, err := fetchChecksum(client, asset)
	if err != nil {
		return err
	}

	target, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate the running binary: %w", err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(target); resolveErr == nil {
		target = resolved
	}

	if err := downloadVerifyInstall(client, asset, wantSum, target); err != nil {
		return err
	}
	fmt.Printf("installed %s -> %s\n", latest, target)

	if *noRestart {
		fmt.Println("skipped service restart (--no-restart); restart it to run the new version")

		return nil
	}

	if err := service.Restart(service.CurrentGOOS()); err != nil {
		fmt.Fprintln(os.Stderr, "warn: could not restart the service automatically:", err)
		fmt.Println("restart it manually to run the new version")

		return nil
	}
	fmt.Println("service restarted")

	return nil
}

// fetchChecksum downloads SHA256SUMS through the /dl proxy and returns the
// expected hex digest for asset.
func fetchChecksum(client *api.Client, asset string) (string, error) {
	body, err := client.DownloadAsset("SHA256SUMS")
	if err != nil {
		return "", err
	}
	defer body.Close()

	data, err := io.ReadAll(io.LimitReader(body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read checksums: %w", err)
	}

	return parseChecksum(data, asset)
}

// parseChecksum finds the SHA-256 digest for asset in the SHA256SUMS body,
// whose lines are "<hex>  <filename>" as produced by sha256sum.
func parseChecksum(sums []byte, asset string) (string, error) {
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		// sha256sum prefixes the name with '*' in binary mode; tolerate it
		if strings.TrimPrefix(fields[1], "*") == asset {
			return fields[0], nil
		}
	}

	return "", fmt.Errorf("no checksum for %q in SHA256SUMS", asset)
}

// downloadVerifyInstall streams asset to a temp file next to target, verifies
// its SHA-256 against wantSum, and atomically renames it over target. The temp
// file lives in target's directory so the rename stays on one filesystem.
func downloadVerifyInstall(client *api.Client, asset, wantSum, target string) error {
	body, err := client.DownloadAsset(asset)
	if err != nil {
		return err
	}
	defer body.Close()

	tmp, err := os.CreateTemp(filepath.Dir(target), ".foreman-runner-update-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	hasher := sha256.New()
	if _, err := io.Copy(tmp, io.TeeReader(body, hasher)); err != nil {
		tmp.Close()

		return fmt.Errorf("download %s: %w", asset, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", tmpName, err)
	}

	got := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(got, wantSum) {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", asset, got, wantSum)
	}

	if err := os.Chmod(tmpName, 0o755); err != nil {
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("replace %s: %w", target, err)
	}

	return nil
}
