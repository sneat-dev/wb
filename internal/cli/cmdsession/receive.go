package cmdsession

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/sessionrun"
	"github.com/spf13/cobra"
	"io"
)

const maxSessionReceiveBytes = 1 << 20

func NewReceive(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var format string
	command := &cobra.Command{
		Use:   "receive",
		Short: "Receive an exact session move and return its durable target receipt",
		Long: `Receive one portable session handoff from exact stdin bytes.

The receiver authenticates its target against the validated remote.machine in
the local wb.yaml, admits the exact request bytes idempotently, fetches the
declared branch directly, and creates or verifies one clean worktree pinned to
the exact bundle commit. It starts the fixed requested harness through a
preallocated WB identity in detached tmux, records the linked target Work Log,
and returns a receipt only after the successor is live. Replaying completed
bytes returns the immutable receipt without launching or consulting Git.
Predecessor custody remains a source-side acknowledgement transaction.`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			raw, err := readSessionReceiveRequest(command)
			if err != nil {
				return err
			}
			result, err := deps.Receive(command.Context(), sessionrun.ReceiveRequest{ProjectsRoot: runtime.Flags().ProjectsRoot, Raw: raw})
			if err != nil {
				return err
			}
			if format == "json" {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(result)
			}
			if result.Receipt != nil {
				if result.Successor != nil {
					_, err = fmt.Fprintf(command.OutOrStdout(), "completed handoff %s for successor %s in tmux %s at exact commit %s; durable target receipt recorded\n",
						result.Request.HandoffID, result.Receipt.SuccessorWBSessionID, result.Receipt.TmuxName, result.Receipt.PinnedCommit)
					return err
				}
				_, err = fmt.Fprintf(command.OutOrStdout(), "replayed completed handoff %s receipt for successor %s in tmux %s\n",
					result.Request.HandoffID, result.Receipt.SuccessorWBSessionID, result.Receipt.TmuxName)
				return err
			}
			if result.Successor != nil {
				_, err = fmt.Fprintf(command.OutOrStdout(), "handoff %s started successor %s in tmux %s at exact commit %s; predecessor custody remains active pending a receipt\n",
					result.Request.HandoffID, result.Successor.WBSessionID, result.Successor.TmuxName, result.Successor.PinnedCommit)
				return err
			}
			worktree := ""
			if result.Worktree != nil {
				worktree = result.Worktree.WorktreeDir
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "handoff %s phase %s at pinned target worktree %s\n",
				result.Request.HandoffID, result.Phase, worktree)
			return err
		},
	}
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}
func readSessionReceiveRequest(command *cobra.Command) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(command.InOrStdin(), maxSessionReceiveBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read session receive request: %w", err)
	}
	if len(raw) > maxSessionReceiveBytes {
		return nil, fmt.Errorf("session receive request exceeds %d bytes", maxSessionReceiveBytes)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("session receive request must not be empty")
	}
	return raw, nil
}
func NewReceivePark(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var format string
	command := &cobra.Command{
		Use:   "receive-park",
		Short: "Receive an exact parked-session bundle and return its durable target receipt",
		Long: `Receive one canonical parked-session envelope from bounded stdin.

The receiver authenticates the declared target against the local remote.machine,
admits the exact envelope bytes into a private no-follow aggregate, reconstructs
every exact pushed member, and creates every target Work Log claim before one
successor is released. It returns one durable receipt only after all members are
attached to that successor. Private continuation material is never printed.`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			raw, err := readSessionReceiveParkEnvelope(command)
			if err != nil {
				return err
			}
			result, err := deps.ReceivePark(command.Context(), sessionrun.ReceiveRequest{ProjectsRoot: runtime.Flags().ProjectsRoot, Raw: raw})
			if err != nil {
				return err
			}
			if format == "json" {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(result)
			}
			if result.Receipt == nil {
				return fmt.Errorf("park resume %s ended at phase %s without a durable receipt", result.ResumeID, result.Phase)
			}
			verb := "completed"
			if result.Replay {
				verb = "replayed"
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "%s park resume %s for successor %s with %d exact members; durable target receipt recorded\n",
				verb, result.ResumeID, result.Receipt.SuccessorWBSessionID, len(result.Receipt.Members))
			return err
		},
	}
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}
func readSessionReceiveParkEnvelope(command *cobra.Command) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(command.InOrStdin(), sessionpark.MaxEnvelopeBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read parked-session receive envelope: %w", err)
	}
	if len(raw) > sessionpark.MaxEnvelopeBytes {
		return nil, fmt.Errorf("park resume envelope exceeds %d bytes", sessionpark.MaxEnvelopeBytes)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("park resume envelope must not be empty")
	}
	return raw, nil
}
