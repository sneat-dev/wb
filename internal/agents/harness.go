package agents

import (
	"fmt"
	"io"
	"time"
)

// Harness capability vocabulary. A capability is declared by the adapter, not
// guessed from the configuration, so an unsupported setting is refused or
// recorded instead of being silently dropped.
const (
	EffortConfig      = "config"
	EffortVariant     = "variant"
	EffortUnsupported = "unsupported"

	TaskDeliveryStdin       = "stdin"
	TaskDeliveryArgv        = "argv"
	TaskDeliveryPrivateFile = "private-file"

	UsageFromEvents      = "events"
	UsageUnavailable     = "unavailable"
	CostHarnessEstimated = "harness-estimated"
	CostUnavailable      = "unavailable"

	CredentialWBInjected   = "wb-injected"
	CredentialHarnessStore = "harness-store"

	IsolationPrivateHome = "private-home"
	IsolationUserHome    = "user-home"
	// IsolationLinuxContainer is the isolation a contained execution actually
	// provides: the worker runs inside the verified Linux container, not in the
	// host user's home.
	IsolationLinuxContainer = "linux-container"
)

// containedHarnessCapabilities projects the adapter's host capabilities onto
// the one contained execution this build runs. It touches only the existing
// record/result axes, so no new record field is introduced, and it is applied
// only when a mandatory execution policy actually admits a contained run. An
// adapter's default (legacy host) capabilities stay untouched without a policy.
func containedHarnessCapabilities(base HarnessCapabilities) HarnessCapabilities {
	base.Credential = CredentialWBInjected
	base.Isolation = IsolationLinuxContainer
	return base
}

// HarnessCapabilities is what WB can prove about one adapter. It is recorded on
// every run so a comparison can state which differences were not controlled.
type HarnessCapabilities struct {
	Harness      string `json:"harness"`
	JSONEvents   bool   `json:"json_events"`
	TaskDelivery string `json:"task_delivery"`
	TokenUsage   string `json:"token_usage"`
	Cost         string `json:"cost"`
	Effort       string `json:"effort"`
	Credential   string `json:"credential_source"`
	Isolation    string `json:"isolation"`
	// Version is filled from the installed binary at dispatch time; it is
	// empty in a dry run, which starts no process.
	Version string `json:"version,omitempty"`
}

// HarnessCapabilityFor answers the effort question without instantiating an
// adapter, so configuration validation and planning share one answer.
func HarnessCapabilityFor(harness string) string {
	switch harness {
	case HarnessCodex, HarnessAntigravity:
		return EffortConfig
	case HarnessOpenCode:
		return EffortVariant
	default:
		return EffortUnsupported
	}
}

// Tokens is usage exactly as the harness reported it. Every field is a pointer
// so "not reported" stays distinguishable from a reported zero.
type Tokens struct {
	Input      *int `json:"input,omitempty"`
	Output     *int `json:"output,omitempty"`
	CacheRead  *int `json:"cache_read,omitempty"`
	CacheWrite *int `json:"cache_write,omitempty"`
	Reasoning  *int `json:"reasoning,omitempty"`
}

// HarnessSummary is what the run owner extracts from one harness stream. It is
// execution metadata only: nothing here judges whether the work is correct.
type HarnessSummary struct {
	TurnCompleted bool
	TurnFailed    bool
	// Tokens is nil when the harness reported no usage at all; a harness that
	// reports zeros fills the pointers with zeros.
	Tokens *Tokens
	// CostUSD is a per-run cost the harness itself estimated. It is nil when the
	// harness reports none, and zero values are rejected by CostRecord.
	CostUSD *float64
	// CostNote explains a reported-but-unusable cost, such as a zero cost on a
	// subscription plan.
	CostNote  string
	ToolCalls int
	// Diagnostics are non-fatal harness messages worth keeping in the record.
	Diagnostics []string
	// MalformedLines counts lines that look like harness events but could not
	// be decoded. Ordinary non-JSON output is a harness's own stderr sharing the
	// file and is expected; a malformed *event* is not.
	MalformedLines int
	// FinalMessage is set by adapters whose only output is the final message,
	// such as `dsh`.
	FinalMessage string
}

// Launch is everything an adapter needs to build one non-interactive run.
type Launch struct {
	Timeout         time.Duration
	RunDir          string
	WorktreeDir     string
	Model           string
	Variant         string
	Effort          string
	ProviderName    string
	Provider        Provider
	LastMessagePath string
	// Task is used only by adapters that deliver the task on the command line.
	Task string
}

// HarnessLaunch is the exact process an adapter wants WB to start. Nothing about
// a harness's flag spelling lives above this boundary.
type HarnessLaunch struct {
	Argv  []string
	Env   []string
	Stdin string
	// PrivateFiles are paths the adapter wrote for this launch, recorded so a
	// reader can find them; they live under the private run directory.
	PrivateFiles []string
	// SplitStderr keeps stderr out of the captured result stream. An adapter
	// whose stdout *is* the final message needs it: merged diagnostics would
	// become part of the answer.
	SplitStderr bool
}

// Harness is one coding-agent adapter. Each implementation has its own file
// in this package and a name in supportedHarnesses.
type Harness interface {
	Name() string
	Capabilities() HarnessCapabilities
	Prepare(Launch) (HarnessLaunch, error)
	Summarize(io.Reader) HarnessSummary
	Actions(io.Reader, int) []string
}

// HarnessFor returns the adapter for a harness name.
func HarnessFor(name string) (Harness, error) {
	switch name {
	case HarnessAntigravity:
		return antigravityHarness{}, nil
	case HarnessCodex:
		return codexHarness{}, nil
	case HarnessOpenCode:
		return openCodeHarness{}, nil
	case HarnessDSH:
		return dshHarness{}, nil
	default:
		return nil, fmt.Errorf("harness %q is not supported by this WB build; supported harnesses: codex, opencode, dsh, antigravity", name)
	}
}

// harnessExecutable separates the configured harness identity from its CLI name.
func harnessExecutable(name string) string {
	if name == HarnessAntigravity {
		return "agy"
	}
	return name
}
