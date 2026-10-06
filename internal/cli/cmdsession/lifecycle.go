package cmdsession

import (
	"encoding/json"
	"fmt"
	"github.com/sneat-dev/wb/internal/cli/sessionview"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/continuationinput"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/sessionrun"
	"github.com/spf13/cobra"
	"strings"
)

func NewRegister(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var record session.Record
	var joinWorktree string
	command := &cobra.Command{
		Use:   "register",
		Short: "Announce this agent session so later WB writes can be attributed to it",
		Long: `Announce this agent session so later WB writes can be attributed to it.

Run it once when a session starts. WB assigns a stable WB session ID, records
the declared identity along with its own version and binary path, and evaluates
liveness from the PID whenever the record is read. Re-registering the same PID
without --wb-session-id preserves its WB identity.

The PID must be the agent process, not the shell that runs this command. From a
harness tool call that is the shell's parent:

  wb session register --pid $PPID --runtime claude-code --model <model>

A start-up hook cannot supply it: hooks run in an isolated subprocess whose
parent is an intermediate shell rather than the agent, so a hook should prompt
the agent to register rather than guess a PID on its behalf.

Codex/live-harness setup: make registration the first WB command issued by the
agent, before any mutating command:

  wb session register --pid $PPID --runtime codex --model <exact-model>

On systems where the shell tail-execs its final command, WB may see the live
Codex app-server as its direct parent. WB accepts that parent only when its
kernel-reported executable is codex and its process role is app-server. For a
shell-safe form that also works with older WB builds, keep the shell alive:

  wb session register --pid "$PPID" --runtime codex --model <exact-model>; status=$?; exit "$status"

Do not substitute $$ (the intermediate shell) or register WB's own PID. An
agent-mode create requires this live registration; for an intentional human
operation use --mode manual --initiator <human> instead.

Registering again for the same PID replaces the record, so a session that
corrects its model does not have to clean up after itself.`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			written, err := deps.Register(command.Context(), sessionrun.RegisterRequest{ProjectsRoot: runtime.Flags().ProjectsRoot, Record: record})
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(command.OutOrStdout(), "registered %s (pid %d) using wb %s\n", sessionLabel(written), written.PID, written.WBVersion); err != nil {
				return err
			}
			if joinWorktree != "" {
				if err := deps.Join(command.Context(), joinWorktree); err != nil {
					return fmt.Errorf("registered but not joined: %w", err)
				}
				if _, err := fmt.Fprintf(command.OutOrStdout(), "joined %s\n", joinWorktree); err != nil {
					return err
				}
			}
			return nil

		},
	}
	command.Flags().IntVar(&record.PID, "pid", 0, "process id of the agent session, e.g. $PPID from a tool call")
	command.Flags().StringVar(&record.WBSessionID, "wb-session-id", "", "preallocated stable WB session ID (normally generated automatically)")
	command.Flags().StringVar(&record.Machine, "machine", "", "canonical WB machine name (default: local hostname)")
	command.Flags().StringVar(&record.Runtime, "runtime", "", "harness running the agent, e.g. claude-code, copilot-cli, codex")
	command.Flags().StringVar(&record.Model, "model", "", "model identifier driving the session")
	command.Flags().StringVar(&record.NativeHarnessID, "native-harness-id", "", "native session ID, when the harness exposes one")
	command.Flags().StringVar(&record.AgentID, "agent-id", "", "legacy alias for --native-harness-id")
	command.Flags().StringVar(&record.TmuxName, "tmux-name", "", "tmux session containing this agent")
	command.Flags().StringVar(&record.PredecessorWBSessionID, "predecessor-wb-session-id", "", "WB session ID that handed off to this session")
	command.Flags().StringVar(&record.HandoffID, "handoff-id", "", "handoff that created this session")
	command.Flags().StringVar(&joinWorktree, "join", "", "join an already owned worktree after registration")
	return command
}
func sessionLabel(record session.Record) string {
	nativeID := record.NativeHarnessID
	if nativeID == "" {
		nativeID = record.AgentID
	}
	switch {
	case record.Runtime != "" && nativeID != "":
		return record.Runtime + "/" + nativeID
	case record.Runtime != "":
		return record.Runtime
	case nativeID != "":
		return nativeID
	default:
		return "an unnamed session"
	}
}
func NewPark(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var contextFile, format, harness, model, wbSessionID string
	var pid int
	var overrideSecrets []string
	command := &cobra.Command{
		Use:   "park",
		Short: "Suspend this agent session with an auditable whole-session checkpoint",
		Long: `Suspend this agent session with an auditable whole-session checkpoint.

Registration is not a precondition. If no session is registered for the calling
process, park registers one first from what it can observe — the agent PID from
--pid, WB_AGENT_PID, or the nearest recognised harness above this process; the
runtime and model from --runtime/--model or the WB_AGENT_* environment; the
machine from this hostname — records ` + "`registered_at_park`" + ` on both the session and
its own output, and continues. A runtime or model WB was neither told nor able
to observe is recorded as "unknown"; missing metadata never refuses a park.

  wb session park --context-file <file>

--wb-session-id parks an already-registered session instead, and never creates
a registration.`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			body, err := readParkContext(command, contextFile)
			if err != nil {
				return err
			}
			result, err := deps.Park(command.Context(), sessionrun.ParkRequest{ProjectsRoot: runtime.Flags().ProjectsRoot, Continuation: body, Runtime: harness, Model: model, PID: pid, WBSessionID: wbSessionID, OverrideSecrets: overrideSecrets})
			if err != nil {
				return err
			}
			sessionview.Advisories(command.ErrOrStderr(), result.Warnings)
			sessionview.ParkChecklist(command.ErrOrStderr(), "parked. Continuation is now immutable; confirm it records:")
			if format == "json" {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(result.Output)
			}
			if result.Output.RegisteredAtPark {
				if _, err := fmt.Fprintf(command.OutOrStdout(), "registered_at_park: this session was not registered, so park registered it as %s (pid %d, runtime %s, model %s)\n", result.Output.WBSessionID, result.Registration.PID, result.Registration.Runtime, result.Registration.Model); err != nil {
					return err
				}
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "parked session %s with %d owned worktrees; pickup with wb session pickup %s\n", result.Output.ParkedSessionID, result.Output.MemberCount, result.Output.ParkedSessionID)
			return err

		},
	}
	command.Flags().StringVar(&contextFile, "context-file", "", "bounded agent-authored continuation file, or - for stdin")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().IntVar(&pid, "pid", 0, "process id of the agent session, used only when park has to register it")
	command.Flags().StringVar(&harness, "runtime", "", "harness running the agent, used only when park has to register it")
	command.Flags().StringVar(&model, "model", "", "model identifier driving the session, used only when park has to register it")
	command.Flags().StringVar(&wbSessionID, "wb-session-id", "", "park this already-registered WB session instead of resolving one from this process")
	command.Flags().StringArrayVar(&overrideSecrets, secretOverrideFlagName, nil, secretOverrideFlagHelp)
	return command
}
func readParkContext(command *cobra.Command, path string) ([]byte, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		sessionview.ParkChecklist(command.ErrOrStderr(), "park requires an agent-authored continuation. WB derives observable state itself; record what it cannot observe:")
		return nil, fmt.Errorf("park requires --context-file; use - to read stdin")
	}
	if path == "-" {
		return continuationinput.ReadBounded(command.InOrStdin(), sessionpark.MaxContinuationBytes, "park context")
	}
	return continuationinput.ReadRegularFile(path, sessionpark.MaxContinuationBytes)
}
func NewResume(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var target, via, configPath, format string
	command := &cobra.Command{
		Use:     "resume <parked-session-id>",
		Aliases: []string{"pickup"},
		Short:   "Resume a parked session as one fresh successor session",
		Long: `Resume a parked session as one fresh successor session.

Use the parked_session_id returned by wb session park or shown for the parked
row by wb session list --format json. wb_session_id identifies the source
agent session and is not a resume argument. pickup is an alias of resume.

Before a fresh local resume claims a route or changes custody, WB verifies the
fixed tmux, harness, and WB executables. If the released harness exits during
startup, WB retains its exit status and bounded terminal diagnostic for the
exact retryable attempt.`,
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			output, err := deps.Resume(command.Context(), sessionrun.ResumeRequest{ProjectsRoot: runtime.Flags().ProjectsRoot, ParkedSessionID: args[0], Target: target, Via: via, ConfigPath: configPath, Stderr: command.ErrOrStderr()})
			if err != nil {
				return err
			}
			return writeSessionResumeOutput(command, format, output)

		},
	}
	command.Flags().StringVar(&target, "to", "", "target WB machine for cross-machine resume")
	command.Flags().StringVar(&via, "via", "", "resume courier (ssh)")
	command.Flags().StringVar(&configPath, "config", "", "path to wb.yaml (reserved for cross-machine courier configuration)")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}
func writeSessionResumeOutput(command *cobra.Command, format string, output sessionrun.ResumeResult) error {
	if format == "json" {
		encoder := json.NewEncoder(command.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(output)
	}
	verb := "resumed"
	if output.Replay {
		verb = "replayed"
	}
	_, err := fmt.Fprintf(command.OutOrStdout(), "%s parked session %s as successor %s on %s with %d worktrees\n",
		verb, output.ParkedSessionID, output.SuccessorWBSessionID, output.TargetMachine, output.MemberCount)
	return err
}
