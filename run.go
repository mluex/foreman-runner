package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/mluex/foreman-runner/internal/agent"
	"github.com/mluex/foreman-runner/internal/api"
	"github.com/mluex/foreman-runner/internal/config"
	"github.com/mluex/foreman-runner/internal/session"
	"github.com/mluex/foreman-runner/internal/system"
)

// cmdRun is the runner daemon: it loads the enrolled config, sends signed
// heartbeats, and polls for tasks. A claimed task's signature is verified
// against the owner's public key before the agent is launched; the agent's
// exit code is reported back when it finishes.
func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	var (
		configPath   = fs.String("config", config.DefaultPath(), "path to the runner config file")
		insecure     = fs.Bool("insecure", false, "skip TLS certificate verification (dev/self-signed only)")
		interval     = fs.Duration("interval", 30*time.Second, "heartbeat interval")
		pollInterval = fs.Duration("poll-interval", 5*time.Second, "task poll interval")
		workDir      = fs.String("dir", "", "working directory agents run in (default: current directory)")
		claudeBin    = fs.String("claude-bin", "claude", "agent binary name or path")
		once         = fs.Bool("once", false, "send a single heartbeat and exit")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("not enrolled? run \"foreman-runner enroll\" first: %w", err)
	}

	privKey, err := decodePrivateKey(cfg.RunnerPrivKey)
	if err != nil {
		return err
	}
	userPubKey, err := base64.StdEncoding.DecodeString(cfg.UserPubKey)
	if err != nil || len(userPubKey) != ed25519.PublicKeySize {
		return fmt.Errorf("invalid user public key in config")
	}

	dir := *workDir
	if dir == "" {
		if dir, err = os.Getwd(); err != nil {
			return fmt.Errorf("resolve working directory: %w", err)
		}
	}

	client := api.New(cfg.ServerURL, *insecure)

	heartbeat := func() error {
		req := api.HeartbeatRequest{
			RunnerID:  cfg.RunnerID,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Nonce:     base64.StdEncoding.EncodeToString(randomBytes(16)),
			Agents:    agent.Detect(),
			System:    system.Sample(),
		}
		resp, err := client.Heartbeat(cfg.APIToken, privKey, req)
		if err != nil {
			return err
		}
		if resp.UserPubKey != "" && resp.UserPubKey != cfg.UserPubKey {
			cfg.UserPubKey = resp.UserPubKey
			if rotated, decodeErr := base64.StdEncoding.DecodeString(resp.UserPubKey); decodeErr == nil && len(rotated) == ed25519.PublicKeySize {
				userPubKey = rotated
			}
			if saveErr := config.Save(*configPath, cfg); saveErr != nil {
				fmt.Fprintln(os.Stderr, "warn: could not persist rotated user key:", saveErr)
			}
		}
		fmt.Printf("heartbeat ok  agents=%d  server_time=%s\n", len(req.Agents), resp.ServerTime)
		return nil
	}

	if *once {
		return heartbeat()
	}

	fmt.Printf("runner %s heartbeating to %s (heartbeat %s, poll %s)\n", cfg.RunnerID, cfg.ServerURL, *interval, *pollInterval)
	if err := heartbeat(); err != nil {
		fmt.Fprintln(os.Stderr, "heartbeat error:", err)
	}

	heartbeatTicker := time.NewTicker(*interval)
	defer heartbeatTicker.Stop()
	pollTicker := time.NewTicker(*pollInterval)
	defer pollTicker.Stop()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	for {
		select {
		case <-heartbeatTicker.C:
			if err := heartbeat(); err != nil {
				fmt.Fprintln(os.Stderr, "heartbeat error:", err)
			}
		case <-pollTicker.C:
			task, err := client.NextTask(cfg.RunnerID, cfg.APIToken)
			if err != nil {
				fmt.Fprintln(os.Stderr, "poll error:", err)
				continue
			}
			if task != nil {
				runTask(client, cfg, privKey, userPubKey, task, dir, *claudeBin)
			}
		case <-stop:
			fmt.Println("shutting down")
			return nil
		}
	}
}

// runTask verifies a claimed task's signature, launches the agent, waits for it
// to finish, and reports the exit code. An invalid signature is rejected.
func runTask(client *api.Client, cfg *config.Config, privKey ed25519.PrivateKey, userPubKey []byte, task *api.NextTaskResponse, dir, claudeBin string) {
	fmt.Printf("claimed task %s\n", task.TaskID)

	signature, err := base64.StdEncoding.DecodeString(task.Signature)
	if err != nil || !ed25519.Verify(userPubKey, []byte(task.Payload), signature) {
		fmt.Fprintln(os.Stderr, "signature verification failed; rejecting task")
		if rejErr := client.RejectTask(task.TaskID, cfg.APIToken, privKey, "signature verification failed"); rejErr != nil {
			fmt.Fprintln(os.Stderr, "reject error:", rejErr)
		}
		return
	}

	var payload api.TaskPayload
	if err := json.Unmarshal([]byte(task.Payload), &payload); err != nil {
		reject(client, cfg, privKey, task.TaskID, "payload is not valid JSON")
		return
	}

	prompt := payload.Prompt
	if prompt == "" {
		reject(client, cfg, privKey, task.TaskID, "task has no prompt")
		return
	}

	logDir := defaultLogDir()
	exitFile := filepath.Join(logDir, "task-"+task.TaskID+".exit")
	_ = os.Remove(exitFile)

	res, err := session.Launch(session.Spec{
		Name:           "foreman-task-" + task.TaskID,
		TaskID:         task.TaskID,
		Dir:            dir,
		Prompt:         prompt,
		Model:          payload.Model,
		PermissionMode: mapEffort(payload.Effort),
		ClaudeBin:      claudeBin,
		LogDir:         logDir,
		ExitCodeFile:   exitFile,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "launch failed:", err)
		finish(client, cfg, privKey, task.TaskID, 1)
		return
	}

	fmt.Printf("running %s (session %s)\n", task.TaskID, res.Name)

	code, err := session.WaitExit(res.Name, exitFile, time.Second)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wait error:", err)
		code = 1
	}

	finish(client, cfg, privKey, task.TaskID, code)
	fmt.Printf("finished %s exit=%d\n", task.TaskID, code)
}

func reject(client *api.Client, cfg *config.Config, privKey ed25519.PrivateKey, taskID, reason string) {
	if err := client.RejectTask(taskID, cfg.APIToken, privKey, reason); err != nil {
		fmt.Fprintln(os.Stderr, "reject error:", err)
	}
}

func finish(client *api.Client, cfg *config.Config, privKey ed25519.PrivateKey, taskID string, code int) {
	if err := client.FinishTask(taskID, cfg.APIToken, privKey, code); err != nil {
		fmt.Fprintln(os.Stderr, "finish error:", err)
	}
}

// mapEffort maps the task effort to a claude --permission-mode value. The exact
// mapping is provisional (see docs/BRIEFING.md §15); unattended runs default to
// accepting edits.
func mapEffort(effort string) string {
	switch effort {
	case "plan":
		return "plan"
	default:
		return "acceptEdits"
	}
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func decodePrivateKey(encoded string) (ed25519.PrivateKey, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode runner private key: %w", err)
	}
	if len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("invalid runner private key length: got %d, want %d", len(raw), ed25519.PrivateKeySize)
	}
	return ed25519.PrivateKey(raw), nil
}
