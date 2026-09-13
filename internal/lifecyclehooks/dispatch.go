package lifecyclehooks

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/gitremote"
	"github.com/sneat-dev/wb/internal/process"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

const (
	receiptSchemaVersion = 2
	jobSchemaVersion     = 1
	defaultParallelism   = 2
	maxDiagnosticBytes   = 64 * 1024
)

type Event struct {
	Name        string `json:"name"`
	Repository  string `json:"repository"`
	Checkout    string `json:"checkout"`
	OldSHA      string `json:"old_sha,omitempty"`
	NewSHA      string `json:"new_sha"`
	Cause       string `json:"cause"`
	OperationID string `json:"operation_id,omitempty"`
}

type Invocation struct {
	Executor string
	Run      string
	Args     []string
	Dir      string
	Env      []string
	Stdout   io.Writer
	Stderr   io.Writer

	configuredRun string
	executable    os.FileInfo
	event         Event
	checkout      os.FileInfo
}

type Report struct {
	Enqueued  int      `json:"enqueued"`
	Executed  int      `json:"executed"`
	Coalesced int      `json:"coalesced"`
	Warnings  []string `json:"warnings,omitempty"`
}

// Receipt is the private local audit record for one terminal hook attempt.
// Command output stays in bounded 0600 diagnostic files and is never copied
// into the receipt or normal WB output.
type Receipt struct {
	SchemaVersion   int       `json:"schema_version"`
	ID              string    `json:"id"`
	Event           string    `json:"event"`
	Repository      string    `json:"repository"`
	Checkout        string    `json:"checkout"`
	OldSHA          string    `json:"old_sha,omitempty"`
	NewSHA          string    `json:"new_sha"`
	Cause           string    `json:"cause"`
	OperationID     string    `json:"operation_id,omitempty"`
	Executor        string    `json:"executor"`
	CoalescedCount  int       `json:"coalesced_count,omitempty"`
	QueuedAt        time.Time `json:"queued_at"`
	StartedAt       time.Time `json:"started_at"`
	FinishedAt      time.Time `json:"finished_at"`
	DurationMS      int64     `json:"duration_ms"`
	Status          string    `json:"status"`
	Failure         string    `json:"failure,omitempty"`
	Message         string    `json:"message,omitempty"`
	StdoutPath      string    `json:"stdout_path,omitempty"`
	StderrPath      string    `json:"stderr_path,omitempty"`
	StdoutTruncated bool      `json:"stdout_truncated,omitempty"`
	StderrTruncated bool      `json:"stderr_truncated,omitempty"`
}

type WorkerRequest struct {
	ConfigPath  string
	StateDir    string
	ReceiptPath string
}

type Dispatcher struct {
	ConfigPath     string
	StateDir       string
	ReceiptPath    string
	Now            func() time.Time
	Run            func(context.Context, Invocation) error
	EvalSymlinks   func(string) (string, error)
	LaunchWorker   func(WorkerRequest) error
	VerifyCheckout func(Event) (string, os.FileInfo, error)
}

type pending struct {
	event    Event
	name     string
	executor Executor
	count    int
}

func DefaultDispatcher() Dispatcher {
	return Dispatcher{
		ConfigPath:     wbconfig.DefaultPath(),
		StateDir:       defaultStateDir(),
		ReceiptPath:    defaultReceiptPath(),
		Now:            time.Now,
		Run:            runInvocation,
		EvalSymlinks:   filepath.EvalSymlinks,
		LaunchWorker:   launchWorker,
		VerifyCheckout: verifyCheckout,
	}
}

func Dispatch(ctx context.Context, events []Event) (Report, error) {
	return DefaultDispatcher().Dispatch(ctx, events)
}

// Dispatch durably enqueues matching events and returns without waiting for
// external executors. A detached worker is only a wake-up mechanism: queued
// state remains authoritative if the worker cannot start or is interrupted.
func (dispatcher Dispatcher) Dispatch(_ context.Context, events []Event) (Report, error) {
	dispatcher = dispatcher.defaults()
	warnings, warningErr := dispatcher.claimUnseenWarnings(10)
	if warningErr != nil {
		warnings = append(warnings, "read unseen lifecycle-hook failures: "+warningErr.Error())
	}
	items, report, err := dispatcher.plan(events)
	report.Warnings = append(report.Warnings, warnings...)
	if err != nil || len(items) == 0 {
		return report, err
	}
	for _, item := range items {
		coalesced, enqueueErr := dispatcher.enqueue(item)
		if enqueueErr != nil {
			return report, enqueueErr
		}
		report.Enqueued++
		report.Coalesced += coalesced
	}
	if dispatcher.LaunchWorker != nil {
		if _, err := dispatcher.startWorkerIfIdle(); err != nil {
			report.Warnings = append(report.Warnings, "lifecycle hooks are queued but the background worker did not start: "+err.Error())
		}
	}
	return report, nil
}

// Plan reports the executor/checkouts that an event set would enqueue without
// mutating queue state or starting a worker.
func (dispatcher Dispatcher) Plan(events []Event) ([]Planned, Report, error) {
	dispatcher = dispatcher.defaults()
	items, report, err := dispatcher.plan(events)
	planned := make([]Planned, 0, len(items))
	for _, item := range items {
		planned = append(planned, Planned{Executor: item.name, Event: item.event, CoalescedCount: item.count})
	}
	return planned, report, err
}

type Planned struct {
	Executor       string `json:"executor"`
	Event          Event  `json:"event"`
	CoalescedCount int    `json:"coalesced_count"`
}

func (dispatcher Dispatcher) plan(events []Event) ([]pending, Report, error) {
	var report Report
	if len(events) == 0 {
		return nil, report, nil
	}
	cfg, found, err := Load(dispatcher.ConfigPath)
	if err != nil || !found {
		return nil, report, err
	}
	var queue []pending
	positions := map[string]int{}
	for _, event := range events {
		if event.Name != EventCheckoutUpdated || strings.TrimSpace(event.NewSHA) == "" || event.OldSHA == event.NewSHA {
			continue
		}
		event.Repository = strings.ToLower(strings.TrimSpace(event.Repository))
		event.Checkout = filepath.Clean(event.Checkout)
		for _, binding := range cfg.Bindings {
			if !binding.matches(event) {
				continue
			}
			for _, name := range binding.Execute {
				key := queueKey(name, event.Checkout)
				if index, ok := positions[key]; ok {
					queue[index].event.NewSHA = event.NewSHA
					queue[index].event.Cause = event.Cause
					queue[index].event.OperationID = event.OperationID
					queue[index].count++
					continue
				}
				positions[key] = len(queue)
				queue = append(queue, pending{event: event, name: name, executor: cfg.Executors[name], count: 1})
			}
		}
	}
	matchedEvents := make([]Event, 0, len(queue))
	for _, item := range queue {
		matchedEvents = append(matchedEvents, item.event)
	}
	if err := dispatcher.validateControlPaths(matchedEvents); err != nil {
		return nil, report, err
	}
	return queue, report, nil
}

func (executor Executor) timeout() (time.Duration, error) {
	if strings.TrimSpace(executor.Timeout) == "" {
		return 2 * time.Minute, nil
	}
	timeout, err := time.ParseDuration(executor.Timeout)
	if err != nil || timeout <= 0 {
		return 0, fmt.Errorf("timeout %q must be a positive duration", executor.Timeout)
	}
	return timeout, nil
}

func (dispatcher Dispatcher) prepare(item pending) (Invocation, error) {
	trusted, err := dispatcher.inspectExecutable(item.executor.Run)
	if err != nil {
		return Invocation{}, err
	}
	physicalCheckout, checkoutInfo, err := dispatcher.VerifyCheckout(item.event)
	if err != nil {
		return Invocation{}, err
	}
	if pathWithin(physicalCheckout, trusted.resolved) {
		return Invocation{}, errors.New("run must resolve outside the repository checkout")
	}
	operationID := item.event.OperationID
	if operationID == "" {
		operationID = os.Getenv("WB_OPERATION_ID")
	}
	return Invocation{
		Executor: item.name, Run: trusted.resolved, Args: append([]string(nil), item.executor.Args...),
		Dir: physicalCheckout, Env: hookEnvironment(item.event, operationID), configuredRun: item.executor.Run,
		executable: trusted.info, event: item.event, checkout: checkoutInfo,
	}, nil
}

type inspectedExecutable struct {
	resolved string
	info     os.FileInfo
}

func (dispatcher Dispatcher) inspectExecutable(configured string) (inspectedExecutable, error) {
	executable := filepath.Clean(configured)
	if !filepath.IsAbs(executable) {
		return inspectedExecutable{}, errors.New("run must be an absolute executable path")
	}
	resolved, err := dispatcher.EvalSymlinks(executable)
	if err != nil {
		return inspectedExecutable{}, fmt.Errorf("resolve executable: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return inspectedExecutable{}, fmt.Errorf("inspect executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return inspectedExecutable{}, errors.New("run must resolve to a regular file")
	}
	if err := validateTrustedExecutable(resolved, info); err != nil {
		return inspectedExecutable{}, err
	}
	return inspectedExecutable{resolved: resolved, info: info}, nil
}

func (dispatcher Dispatcher) revalidate(invocation Invocation) error {
	current, err := dispatcher.inspectExecutable(invocation.configuredRun)
	if err != nil {
		return fmt.Errorf("revalidate executable immediately before execution: %w", err)
	}
	if current.resolved != invocation.Run || !os.SameFile(invocation.executable, current.info) {
		return errors.New("revalidate executable immediately before execution: executable identity changed")
	}
	checkout, info, err := dispatcher.VerifyCheckout(invocation.event)
	if err != nil {
		return fmt.Errorf("revalidate checkout immediately before execution: %w", err)
	}
	if checkout != invocation.Dir || !os.SameFile(invocation.checkout, info) {
		return errors.New("revalidate checkout immediately before execution: checkout identity changed")
	}
	return nil
}

func runInvocation(ctx context.Context, invocation Invocation) error {
	command := process.CommandContext(ctx, invocation.Run, invocation.Args...) //nolint:gosec // trusted config; no shell
	command.Dir = invocation.Dir
	command.Env = invocation.Env
	command.Stdout = invocation.Stdout
	command.Stderr = invocation.Stderr
	return command.Run()
}

func launchWorker(request WorkerRequest) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	arguments := []string{"hooks", "lifecycle", "run-pending", "--config", request.ConfigPath, "--state-dir", request.StateDir, "--receipt", request.ReceiptPath}
	command := exec.Command(executable, arguments...) //nolint:gosec // current WB executable and fixed argv
	command.Env = os.Environ()
	configureDetached(command)
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer func() { _ = null.Close() }()
	command.Stdin, command.Stdout, command.Stderr = null, null, null
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}

func hookEnvironment(event Event, operationID string) []string {
	env := []string{"PATH=/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"}
	for _, key := range []string{"HOME", "TMPDIR", "LANG", "LC_ALL", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
		if value := os.Getenv(key); value != "" {
			env = append(env, key+"="+value)
		}
	}
	values := map[string]string{
		"WB_HOOK_EVENT": event.Name, "WB_REPOSITORY": event.Repository,
		"WB_CHECKOUT": event.Checkout, "WB_OLD_SHA": event.OldSHA,
		"WB_NEW_SHA": event.NewSHA, "WB_UPDATE_CAUSE": event.Cause,
		"WB_OPERATION_ID": operationID,
	}
	for _, key := range []string{"WB_HOOK_EVENT", "WB_REPOSITORY", "WB_CHECKOUT", "WB_OLD_SHA", "WB_NEW_SHA", "WB_UPDATE_CAUSE", "WB_OPERATION_ID"} {
		if values[key] != "" {
			env = append(env, key+"="+values[key])
		}
	}
	return env
}

func RepositoryIdentity(checkout string) (string, error) {
	origin, err := gitops.OriginURL(checkout)
	if err != nil {
		return "", err
	}
	remote, err := gitremote.Parse(origin)
	if err != nil {
		return "", err
	}
	return strings.ToLower(remote.Identity.Host() + "/" + remote.Identity.Repository), nil
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func defaultStateDir() string {
	return filepath.Join(filepath.Dir(defaultReceiptPath()), "lifecycle-hooks")
}

func defaultReceiptPath() string {
	if stateHome := os.Getenv("XDG_STATE_HOME"); stateHome != "" {
		return filepath.Join(stateHome, "wb", "lifecycle-hook-events.jsonl")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".wb", "lifecycle-hook-events.jsonl")
	}
	return filepath.Join(home, ".local", "state", "wb", "lifecycle-hook-events.jsonl")
}

func (dispatcher Dispatcher) defaults() Dispatcher {
	defaults := DefaultDispatcher()
	if dispatcher.ConfigPath == "" {
		dispatcher.ConfigPath = defaults.ConfigPath
	}
	if dispatcher.ReceiptPath == "" {
		dispatcher.ReceiptPath = defaults.ReceiptPath
	}
	if dispatcher.StateDir == "" {
		if dispatcher.ReceiptPath != defaults.ReceiptPath {
			dispatcher.StateDir = filepath.Join(filepath.Dir(dispatcher.ReceiptPath), "lifecycle-hooks")
		} else {
			dispatcher.StateDir = defaults.StateDir
		}
	}
	if dispatcher.Now == nil {
		dispatcher.Now = defaults.Now
	}
	if dispatcher.Run == nil {
		dispatcher.Run = defaults.Run
	}
	if dispatcher.EvalSymlinks == nil {
		dispatcher.EvalSymlinks = defaults.EvalSymlinks
	}
	if dispatcher.LaunchWorker == nil {
		dispatcher.LaunchWorker = defaults.LaunchWorker
	}
	if dispatcher.VerifyCheckout == nil {
		dispatcher.VerifyCheckout = defaults.VerifyCheckout
	}
	return dispatcher
}

func receiptID(now time.Time) string {
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		return fmt.Sprintf("%d", now.UnixNano())
	}
	return now.Format("20060102T150405.000000000Z") + "-" + hex.EncodeToString(random)
}

func failureClass(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return "exit"
	}
	return "configuration"
}
