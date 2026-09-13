package lifecyclehooks

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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
	"github.com/sneat-dev/wb/internal/wbconfig"
)

type Event struct {
	Name        string
	Repository  string
	Checkout    string
	OldSHA      string
	NewSHA      string
	Cause       string
	OperationID string
}

type Invocation struct {
	Executor string
	Run      string
	Args     []string
	Dir      string
	Env      []string
}

type Report struct {
	Executed  int
	Coalesced int
	Warnings  []string
}

type receipt struct {
	SchemaVersion  int       `json:"schema_version"`
	ID             string    `json:"id"`
	Event          string    `json:"event"`
	Repository     string    `json:"repository"`
	Checkout       string    `json:"checkout"`
	OldSHA         string    `json:"old_sha,omitempty"`
	NewSHA         string    `json:"new_sha"`
	Cause          string    `json:"cause"`
	OperationID    string    `json:"operation_id,omitempty"`
	Executor       string    `json:"executor"`
	CoalescedCount int       `json:"coalesced_count,omitempty"`
	StartedAt      time.Time `json:"started_at"`
	FinishedAt     time.Time `json:"finished_at"`
	DurationMS     int64     `json:"duration_ms"`
	Status         string    `json:"status"`
	Failure        string    `json:"failure,omitempty"`
}

type Dispatcher struct {
	ConfigPath   string
	ReceiptPath  string
	Now          func() time.Time
	Run          func(context.Context, Invocation) error
	EvalSymlinks func(string) (string, error)
}

type pending struct {
	event    Event
	name     string
	executor Executor
	count    int
}

func DefaultDispatcher() Dispatcher {
	return Dispatcher{
		ConfigPath:   wbconfig.DefaultPath(),
		ReceiptPath:  defaultReceiptPath(),
		Now:          time.Now,
		Run:          runInvocation,
		EvalSymlinks: filepath.EvalSymlinks,
	}
}

func Dispatch(ctx context.Context, events []Event) (Report, error) {
	return DefaultDispatcher().Dispatch(ctx, events)
}

func (dispatcher Dispatcher) Dispatch(ctx context.Context, events []Event) (Report, error) {
	var report Report
	if len(events) == 0 {
		return report, nil
	}
	cfg, found, err := Load(dispatcher.ConfigPath)
	if err != nil || !found {
		return report, err
	}
	if dispatcher.Now == nil {
		dispatcher.Now = time.Now
	}
	if dispatcher.Run == nil {
		dispatcher.Run = runInvocation
	}
	if dispatcher.EvalSymlinks == nil {
		dispatcher.EvalSymlinks = filepath.EvalSymlinks
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
				key := name + "\x00" + event.Checkout
				if index, ok := positions[key]; ok {
					queue[index].event.NewSHA = event.NewSHA
					queue[index].event.Cause = event.Cause
					queue[index].event.OperationID = event.OperationID
					queue[index].count++
					report.Coalesced++
					continue
				}
				positions[key] = len(queue)
				queue = append(queue, pending{event: event, name: name, executor: cfg.Executors[name], count: 1})
			}
		}
	}

	for _, item := range queue {
		started := dispatcher.Now().UTC()
		rec := receipt{
			SchemaVersion: 1, ID: receiptID(started), Event: item.event.Name,
			Repository: item.event.Repository, Checkout: item.event.Checkout,
			OldSHA: item.event.OldSHA, NewSHA: item.event.NewSHA, Cause: item.event.Cause,
			OperationID: item.event.OperationID, Executor: item.name,
			CoalescedCount: item.count, StartedAt: started,
		}
		invocation, prepareErr := dispatcher.prepare(item)
		if prepareErr == nil {
			timeout, _ := item.executor.timeout()
			runContext, cancel := context.WithTimeout(ctx, timeout)
			prepareErr = dispatcher.Run(runContext, invocation)
			cancel()
		}
		finished := dispatcher.Now().UTC()
		rec.FinishedAt = finished
		rec.DurationMS = finished.Sub(started).Milliseconds()
		if prepareErr != nil {
			rec.Status = "failed"
			rec.Failure = failureClass(prepareErr)
			failure := fmt.Errorf("lifecycle hook %s for %s failed: %w", item.name, item.event.Repository, prepareErr)
			report.Warnings = append(report.Warnings, failure.Error())
		} else {
			rec.Status = "succeeded"
			report.Executed++
		}
		if writeErr := appendReceipt(dispatcher.ReceiptPath, rec); writeErr != nil {
			report.Warnings = append(report.Warnings, fmt.Sprintf("record lifecycle hook receipt: %v", writeErr))
		}
	}
	return report, nil
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
	executable := filepath.Clean(item.executor.Run)
	if !filepath.IsAbs(executable) {
		return Invocation{}, errors.New("run must be an absolute executable path")
	}
	resolved, err := dispatcher.EvalSymlinks(executable)
	if err != nil {
		return Invocation{}, fmt.Errorf("resolve executable: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return Invocation{}, fmt.Errorf("inspect executable: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return Invocation{}, errors.New("run must resolve to a regular executable file")
	}
	checkout, err := filepath.Abs(item.event.Checkout)
	if err != nil {
		return Invocation{}, err
	}
	physicalCheckout, err := dispatcher.EvalSymlinks(checkout)
	if err != nil {
		return Invocation{}, fmt.Errorf("resolve checkout: %w", err)
	}
	if pathWithin(physicalCheckout, resolved) {
		return Invocation{}, errors.New("run must resolve outside the repository checkout")
	}
	operationID := item.event.OperationID
	if operationID == "" {
		operationID = os.Getenv("WB_OPERATION_ID")
	}
	return Invocation{
		Executor: item.name, Run: resolved, Args: append([]string(nil), item.executor.Args...),
		Dir: checkout, Env: hookEnvironment(item.event, operationID),
	}, nil
}

func runInvocation(ctx context.Context, invocation Invocation) error {
	command := exec.CommandContext(ctx, invocation.Run, invocation.Args...) //nolint:gosec // trusted config; no shell
	command.Dir = invocation.Dir
	command.Env = invocation.Env
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	return command.Run()
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

func appendReceipt(path string, value receipt) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	_, err = file.Write(encoded)
	return err
}

func receiptID(now time.Time) string {
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return fmt.Sprintf("%d", now.UnixNano())
	}
	return fmt.Sprintf("%d-%s", now.UnixNano(), hex.EncodeToString(suffix[:]))
}

func failureClass(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return "exit"
	}
	return "invalid-or-unavailable"
}
