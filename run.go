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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mluex/foreman-runner/internal/agent"
	"github.com/mluex/foreman-runner/internal/api"
	"github.com/mluex/foreman-runner/internal/config"
	"github.com/mluex/foreman-runner/internal/enc"
	"github.com/mluex/foreman-runner/internal/logstream"
	"github.com/mluex/foreman-runner/internal/session"
	"github.com/mluex/foreman-runner/internal/system"
	"github.com/mluex/foreman-runner/internal/trust"
)

// cmdRun is the runner daemon: it loads the enrolled config, sends signed
// heartbeats, and polls for tasks. A claimed task's signature is verified
// against the owner's public key before the agent is launched; the agent's
// exit code is reported back when it finishes.
func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	var (
		configPath         = fs.String("config", config.DefaultPath(), "path to the runner config file")
		insecure           = fs.Bool("insecure", false, "skip TLS certificate verification (dev/self-signed only)")
		interval           = fs.Duration("interval", 30*time.Second, "heartbeat interval")
		pollInterval       = fs.Duration("poll-interval", 5*time.Second, "task poll interval")
		cancelPollInterval = fs.Duration("cancel-poll-interval", 3*time.Second, "how often to check for a web-requested cancellation while a task runs")
		workDir            = fs.String("dir", "", "working directory agents run in (default: current directory)")
		claudeBin          = fs.String("claude-bin", "claude", "agent binary name or path")
		once               = fs.Bool("once", false, "send a single heartbeat and exit")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("not enrolled? run \"foreman-runner enroll\" first: %w", err)
	}

	// Runners enrolled before end-to-end encryption have no X25519 keypair.
	// Generate one on first run so the heartbeat can publish the public key and
	// the browser can seal task content to it.
	if cfg.EncPrivKey == "" || cfg.EncPubKey == "" {
		keys, genErr := enc.GenerateKeypair()
		if genErr != nil {
			return fmt.Errorf("generate encryption key: %w", genErr)
		}
		cfg.EncPrivKey = keys.PrivateKey
		cfg.EncPubKey = keys.PublicKey
		if saveErr := config.Save(*configPath, cfg); saveErr != nil {
			return fmt.Errorf("persist encryption key: %w", saveErr)
		}
		fmt.Println("generated an encryption key for this runner")
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
	if dir, err = filepath.Abs(dir); err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}

	// Pre-accept Claude Code's per-directory workspace trust dialog for the
	// working directory. Without this an unattended task launch stalls on the
	// "Is this a project you trust?" prompt.
	if _, err := trust.Seed(trust.DefaultConfigPath(), dir); err != nil {
		fmt.Fprintln(os.Stderr, "warn: could not pre-seed workspace trust:", err)
	}

	client := api.New(cfg.ServerURL, *insecure)

	heartbeat := func() error {
		req := api.HeartbeatRequest{
			RunnerID:  cfg.RunnerID,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Nonce:     base64.StdEncoding.EncodeToString(randomBytes(16)),
			EncPubKey: cfg.EncPubKey,
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
		if resp.UserEncPubKey != "" && resp.UserEncPubKey != cfg.UserEncPubKey {
			cfg.UserEncPubKey = resp.UserEncPubKey
			if saveErr := config.Save(*configPath, cfg); saveErr != nil {
				fmt.Fprintln(os.Stderr, "warn: could not persist user encryption key:", saveErr)
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
				runTask(client, cfg, privKey, userPubKey, task, dir, *claudeBin, *cancelPollInterval)
			}
		case <-stop:
			fmt.Println("shutting down")
			return nil
		}
	}
}

// runTask verifies a claimed task's signature, launches the agent, waits for it
// to finish, and reports the exit code. An invalid signature is rejected. While
// the agent runs it watches for a cancellation requested from the web UI and
// tears the session down when one arrives.
func runTask(client *api.Client, cfg *config.Config, privKey ed25519.PrivateKey, userPubKey []byte, task *api.NextTaskResponse, dir, claudeBin string, cancelPollInterval time.Duration) {
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
	title := payload.Title
	encryptLogs := payload.Enc == "x25519-sealedbox"
	if payload.Enc == "x25519-sealedbox" {
		if cfg.EncPrivKey == "" || cfg.EncPubKey == "" {
			reject(client, cfg, privKey, task.TaskID, "task is encrypted but the runner has no encryption key; re-enroll")
			return
		}
		if cfg.UserEncPubKey == "" {
			reject(client, cfg, privKey, task.TaskID, "task is encrypted but the user encryption key is not known yet; try again shortly")
			return
		}
		decryptedPrompt, err := enc.OpenSealedBase64(payload.Prompt, cfg.EncPubKey, cfg.EncPrivKey)
		if err != nil {
			reject(client, cfg, privKey, task.TaskID, "cannot decrypt prompt: "+err.Error())
			return
		}
		prompt = decryptedPrompt

		if payload.Title != "" {
			decryptedTitle, err := enc.OpenSealedBase64(payload.Title, cfg.EncPubKey, cfg.EncPrivKey)
			if err != nil {
				reject(client, cfg, privKey, task.TaskID, "cannot decrypt title: "+err.Error())
				return
			}
			title = decryptedTitle
		}
	}

	if prompt == "" {
		reject(client, cfg, privKey, task.TaskID, "task has no prompt")
		return
	}

	logDir := defaultLogDir()
	exitFile := filepath.Join(logDir, "task-"+task.TaskID+".exit")
	_ = os.Remove(exitFile)

	// every task runs as a live, interactive session with Remote Control enabled
	// so it is controllable from Claude Web - that is the point. The prompt is
	// passed as the prompt argument (not prefixed with /remote-control, which
	// would be interpreted as the session name), so Claude acts on it directly.
	res, err := session.Launch(session.Spec{
		Name:           "foreman-task-" + task.TaskID,
		TaskID:         task.TaskID,
		Dir:            dir,
		Prompt:         prompt,
		Model:          payload.Model,
		PermissionMode: mapMode(payload.Mode),
		ClaudeBin:      claudeBin,
		RemoteControl:  true,
		SessionName:    remoteControlName(title, task.TaskID),
		LogDir:         logDir,
		ExitCodeFile:   exitFile,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "launch failed:", err)
		finish(client, cfg, privKey, task.TaskID, 1)
		return
	}

	fmt.Printf("running %s (session %s)\n", task.TaskID, res.Name)

	stopLogs := make(chan struct{})
	var logsDone sync.WaitGroup
	logsDone.Add(1)
	go func() {
		defer logsDone.Done()
		logstream.Stream(res.LogFile, 2*time.Second, func(seq int, chunk string) error {
			if encryptLogs {
				sealed, sealErr := enc.SealBase64(chunk, cfg.UserEncPubKey)
				if sealErr != nil {
					return sealErr
				}
				chunk = sealed
			}

			return client.SendLog(task.TaskID, cfg.APIToken, privKey, seq, chunk)
		}, stopLogs)
	}()

	stopCancelWatch := make(chan struct{})
	go watchForCancel(client, cfg, task.TaskID, res.Name, cancelPollInterval, stopCancelWatch)

	code, err := session.WaitExit(res.Name, exitFile, time.Second)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wait error:", err)
		code = 1
	}

	close(stopCancelWatch)

	// stop tailing and let the final flush finish before reporting completion,
	// so a finished task has all of its logs persisted server-side
	close(stopLogs)
	logsDone.Wait()

	finish(client, cfg, privKey, task.TaskID, code)
	fmt.Printf("finished %s exit=%d\n", task.TaskID, code)
}

// watchForCancel polls the server for a cancellation requested from the web UI
// while a task runs. On the first request it gracefully shuts the session down;
// WaitExit then observes the session ending and the task is reported finished,
// which the server records as a cancellation. It returns when the session ends
// (stop is closed) or once it has triggered a shutdown.
func watchForCancel(client *api.Client, cfg *config.Config, taskID, sessionName string, interval time.Duration, stop <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			status, err := client.TaskStatus(taskID, cfg.APIToken)
			if err != nil {
				fmt.Fprintln(os.Stderr, "cancel-poll error:", err)

				continue
			}
			if status.CancelRequested {
				fmt.Printf("cancellation requested for %s; shutting the session down\n", taskID)
				if err := session.Cancel(sessionName, 10*time.Second); err != nil {
					fmt.Fprintln(os.Stderr, "cancel error:", err)
				}

				return
			}
		}
	}
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

// remoteControlName derives the Remote Control session display name from the
// task title, falling back to a task-derived name. It returns a single-line
// value that never starts with a dash, so it cannot be mistaken for an option
// when passed as the explicit name of --remote-control.
func remoteControlName(title, taskID string) string {
	name := strings.TrimSpace(strings.SplitN(title, "\n", 2)[0])
	if name == "" || strings.HasPrefix(name, "-") {
		return "foreman-task-" + taskID
	}

	return name
}

// mapMode maps the task mode selection (the web UI offers auto, plan, and
// code-only) to a claude --permission-mode value. Unattended runs need a
// non-interactive mode or the session stalls on the first permission prompt, so
// auto is both the "auto" selection and the fallback - matching the PoC spawn
// default that boots straight into a live session. See docs/BRIEFING.md §15.
func mapMode(mode string) string {
	switch mode {
	case "plan":
		return "plan"
	case "code-only":
		return "acceptEdits"
	default:
		return "auto"
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
