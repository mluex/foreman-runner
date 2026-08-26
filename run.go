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
	"github.com/mluex/foreman-runner/internal/taskstate"
	"github.com/mluex/foreman-runner/internal/trust"
)

// sessionPrefix is what every task session is named after, which is also how
// the runner recognizes its own sessions again after a restart.
const sessionPrefix = "foreman-task-"

// encSealedBox is the payload encryption marker; when a task carries it, prompt
// and title arrive as sealed boxes and log chunks go back sealed as well.
const encSealedBox = "x25519-sealedbox"

// cmdRun is the runner daemon: it loads the enrolled config, sends signed
// heartbeats, and polls for tasks. A claimed task's signature is verified
// against the owner's public key before the agent is launched; the agent's
// exit code is reported back when it finishes. Tasks are supervised in their
// own goroutines up to the configured slot limit, so a long-lived session never
// blocks the queue or the heartbeat.
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
		maxTasks           = fs.Int("max-tasks", 0, "tasks to run at the same time (0 = max_tasks from the config file)")
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

	slots := *maxTasks
	if slots < 1 {
		slots = cfg.TaskSlots()
	}

	sup := &supervisor{
		client:             client,
		cfg:                cfg,
		privKey:            privKey,
		userPubKey:         userPubKey,
		dir:                dir,
		claudeBin:          *claudeBin,
		cancelPollInterval: *cancelPollInterval,
		logDir:             defaultLogDir(),
		maxTasks:           slots,
		active:             make(map[string]struct{}),
	}

	fmt.Printf("runner %s heartbeating to %s (heartbeat %s, poll %s, up to %d tasks at once)\n", cfg.RunnerID, cfg.ServerURL, *interval, *pollInterval, slots)
	if err := heartbeat(); err != nil {
		fmt.Fprintln(os.Stderr, "heartbeat error:", err)
	}

	sup.adopt()

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
			sup.claim()
		case <-stop:
			// task sessions are detached and outlive this process; their state
			// files let the next start pick them up where they left off
			fmt.Printf("shutting down (%d task(s) keep running; they are adopted on the next start)\n", sup.inFlight())
			return nil
		}
	}
}

// supervisor owns the runner's in-flight tasks: it claims new ones while slots
// are free, adopts sessions that outlived a previous process, and reports every
// task's logs and exit code back to the server.
type supervisor struct {
	client             *api.Client
	cfg                *config.Config
	privKey            ed25519.PrivateKey
	userPubKey         []byte
	dir                string
	claudeBin          string
	cancelPollInterval time.Duration
	logDir             string
	maxTasks           int

	mu     sync.Mutex
	active map[string]struct{}
}

func (s *supervisor) inFlight() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.active)
}

// track registers a task as in flight, reporting false when this runner is
// already supervising it - a claim the server handed out twice must never end
// up with two supervisors on one session.
func (s *supervisor) track(taskID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, running := s.active[taskID]; running {
		return false
	}
	s.active[taskID] = struct{}{}

	return true
}

func (s *supervisor) untrack(taskID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.active, taskID)
}

// claim takes tasks off the queue until it is empty or every slot is busy. It
// keeps claiming within one tick so a batch of queued tasks starts together
// instead of trickling in one poll interval at a time.
func (s *supervisor) claim() {
	for s.inFlight() < s.maxTasks {
		task, err := s.client.NextTask(s.cfg.RunnerID, s.cfg.APIToken)
		if err != nil {
			fmt.Fprintln(os.Stderr, "poll error:", err)

			return
		}
		if task == nil {
			return
		}
		if !s.track(task.TaskID) {
			fmt.Fprintf(os.Stderr, "task %s is already running here; ignoring the duplicate claim\n", task.TaskID)

			return
		}

		go func(claimed *api.NextTaskResponse) {
			defer s.untrack(claimed.TaskID)
			s.runTask(claimed)
		}(task)
	}
}

// adoptCandidate is a task the runner may have to pick up again: what we know
// about it from disk, and whether that bookkeeping existed at all.
type adoptCandidate struct {
	state taskstate.State
	known bool
}

// adopt picks up task sessions that outlived the runner process - a restart, a
// self-update, a crash. Without it their output stops flowing and the server
// keeps them on "running" forever. Sessions that ended while the runner was
// down are closed out from their recorded state instead.
func (s *supervisor) adopt() {
	candidates := make(map[string]adoptCandidate)

	for _, stored := range taskstate.List(s.logDir) {
		candidates[stored.TaskID] = adoptCandidate{state: stored, known: true}
	}

	names, err := session.Names(sessionPrefix)
	if err != nil {
		fmt.Fprintln(os.Stderr, "warn: could not list tmux sessions:", err)
	}
	for _, name := range names {
		taskID := strings.TrimPrefix(name, sessionPrefix)
		entry := candidates[taskID]
		entry.state.TaskID = taskID
		entry.state.Session = name
		candidates[taskID] = entry
	}

	adopted := 0
	for taskID, entry := range candidates {
		status, err := s.client.TaskStatus(taskID, s.cfg.APIToken)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warn: could not check task %s: %v\n", taskID, err)

			continue
		}
		if !status.Active() {
			// the server has already closed this one out; drop our bookkeeping
			// and leave whatever session is still up to its operator
			taskstate.Remove(s.logDir, taskID)

			continue
		}

		st := entry.state
		if !entry.known {
			// no bookkeeping for this session (a runner from before state files,
			// or a cleared state dir): resume numbering above what the server
			// stored and skip the backlog we cannot map to a sequence
			st.LogFile = filepath.Join(s.logDir, "task-"+taskID+".log")
			st.ExitFile = filepath.Join(s.logDir, "task-"+taskID+".exit")
			st.EncryptLogs = status.Enc == encSealedBox
			st.Seq = status.LastLogSeq
			st.Offset = fileSize(st.LogFile)
		}
		if st.Session == "" {
			st.Session = sessionPrefix + taskID
		}

		if !s.track(taskID) {
			continue
		}
		adopted++
		fmt.Printf("adopted task %s (session %s, resuming at seq %d)\n", taskID, st.Session, st.Seq)

		go func(adoptedState taskstate.State) {
			defer s.untrack(adoptedState.TaskID)
			s.supervise(adoptedState)
		}(st)
	}

	if adopted > 0 {
		fmt.Printf("adopted %d task(s) from a previous run\n", adopted)
	}
}

// runTask verifies a claimed task's signature, launches the agent, and hands
// the live session to supervise. An invalid signature is rejected.
func (s *supervisor) runTask(task *api.NextTaskResponse) {
	fmt.Printf("claimed task %s\n", task.TaskID)

	signature, err := base64.StdEncoding.DecodeString(task.Signature)
	if err != nil || !ed25519.Verify(s.userPubKey, []byte(task.Payload), signature) {
		fmt.Fprintln(os.Stderr, "signature verification failed; rejecting task")
		if rejErr := s.client.RejectTask(task.TaskID, s.cfg.APIToken, s.privKey, "signature verification failed"); rejErr != nil {
			fmt.Fprintln(os.Stderr, "reject error:", rejErr)
		}

		return
	}

	var payload api.TaskPayload
	if err := json.Unmarshal([]byte(task.Payload), &payload); err != nil {
		reject(s.client, s.cfg, s.privKey, task.TaskID, "payload is not valid JSON")

		return
	}

	prompt := payload.Prompt
	title := payload.Title
	encryptLogs := payload.Enc == encSealedBox
	if payload.Enc == encSealedBox {
		if s.cfg.EncPrivKey == "" || s.cfg.EncPubKey == "" {
			reject(s.client, s.cfg, s.privKey, task.TaskID, "task is encrypted but the runner has no encryption key; re-enroll")

			return
		}
		if s.cfg.UserEncPubKey == "" {
			reject(s.client, s.cfg, s.privKey, task.TaskID, "task is encrypted but the user encryption key is not known yet; try again shortly")

			return
		}
		decryptedPrompt, err := enc.OpenSealedBase64(payload.Prompt, s.cfg.EncPubKey, s.cfg.EncPrivKey)
		if err != nil {
			reject(s.client, s.cfg, s.privKey, task.TaskID, "cannot decrypt prompt: "+err.Error())

			return
		}
		prompt = decryptedPrompt

		if payload.Title != "" {
			decryptedTitle, err := enc.OpenSealedBase64(payload.Title, s.cfg.EncPubKey, s.cfg.EncPrivKey)
			if err != nil {
				reject(s.client, s.cfg, s.privKey, task.TaskID, "cannot decrypt title: "+err.Error())

				return
			}
			title = decryptedTitle
		}
	}

	if prompt == "" {
		reject(s.client, s.cfg, s.privKey, task.TaskID, "task has no prompt")

		return
	}

	exitFile := filepath.Join(s.logDir, "task-"+task.TaskID+".exit")
	_ = os.Remove(exitFile)

	// every task runs as a live, interactive session with Remote Control enabled
	// so it is controllable from Claude Web - that is the point. The prompt is
	// passed as the prompt argument (not prefixed with /remote-control, which
	// would be interpreted as the session name), so Claude acts on it directly.
	res, err := session.Launch(session.Spec{
		Name:           sessionPrefix + task.TaskID,
		TaskID:         task.TaskID,
		Dir:            s.dir,
		Prompt:         prompt,
		Model:          payload.Model,
		PermissionMode: mapMode(payload.Mode),
		ClaudeBin:      s.claudeBin,
		RemoteControl:  true,
		SessionName:    remoteControlName(title, task.TaskID),
		LogDir:         s.logDir,
		ExitCodeFile:   exitFile,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "launch failed:", err)
		finish(s.client, s.cfg, s.privKey, task.TaskID, 1)

		return
	}

	fmt.Printf("running %s (session %s)\n", task.TaskID, res.Name)

	s.supervise(taskstate.State{
		TaskID:      task.TaskID,
		Session:     res.Name,
		LogFile:     res.LogFile,
		ExitFile:    exitFile,
		EncryptLogs: encryptLogs,
	})
}

// supervise streams a live session's output, watches for a cancellation
// requested from the web UI, and reports the exit code once the session ends.
// It resumes from the sequence and offset carried in st, so an adopted session
// continues its log where the previous process left off.
func (s *supervisor) supervise(st taskstate.State) {
	if err := taskstate.Save(s.logDir, st); err != nil {
		fmt.Fprintln(os.Stderr, "warn: could not record task state:", err)
	}

	stopLogs := make(chan struct{})
	var logsDone sync.WaitGroup
	logsDone.Add(1)
	go func() {
		defer logsDone.Done()

		onProgress := func(p logstream.Progress) {
			advanced := st
			advanced.Seq = p.Seq
			advanced.Offset = p.Offset
			if err := taskstate.Save(s.logDir, advanced); err != nil {
				fmt.Fprintln(os.Stderr, "warn: could not record log progress:", err)
			}
		}

		logstream.StreamFrom(st.LogFile, 2*time.Second, logstream.Progress{Seq: st.Seq, Offset: st.Offset}, onProgress, func(seq int, chunk string) error {
			if st.EncryptLogs {
				sealed, sealErr := enc.SealBase64(chunk, s.cfg.UserEncPubKey)
				if sealErr != nil {
					return sealErr
				}
				chunk = sealed
			}

			return s.client.SendLog(st.TaskID, s.cfg.APIToken, s.privKey, seq, chunk)
		}, stopLogs)
	}()

	stopCancelWatch := make(chan struct{})
	go watchForCancel(s.client, s.cfg, st.TaskID, st.Session, s.cancelPollInterval, stopCancelWatch)

	code, err := session.WaitExit(st.Session, st.ExitFile, time.Second)
	if err != nil {
		// a session that ended without leaving an exit code (a reboot, a hard
		// kill) cannot be reported honestly as a success
		fmt.Fprintln(os.Stderr, "wait error:", err)
		code = 1
	}

	close(stopCancelWatch)

	// stop tailing and let the final flush finish before reporting completion,
	// so a finished task has all of its logs persisted server-side
	close(stopLogs)
	logsDone.Wait()

	finish(s.client, s.cfg, s.privKey, st.TaskID, code)
	taskstate.Remove(s.logDir, st.TaskID)
	fmt.Printf("finished %s exit=%d\n", st.TaskID, code)
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

// fileSize returns the size of a file, or 0 when it cannot be stat'ed.
func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}

	return info.Size()
}

// remoteControlName derives the Remote Control session display name from the
// task title, falling back to a task-derived name. It returns a single-line
// value that never starts with a dash, so it cannot be mistaken for an option
// when passed as the explicit name of --remote-control.
func remoteControlName(title, taskID string) string {
	name := strings.TrimSpace(strings.SplitN(title, "\n", 2)[0])
	if name == "" || strings.HasPrefix(name, "-") {
		return sessionPrefix + taskID
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
