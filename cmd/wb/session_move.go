package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessioncourier"
	"github.com/sneat-dev/wb/internal/sessioncustody"
	"github.com/sneat-dev/wb/internal/sessionlaunch"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionreceive"
	"github.com/sneat-dev/wb/internal/wbconfig"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

const maxSessionHandoverBytes = 1 << 20

type sessionMoveDependencies struct {
	defaultConfigPath func() string
	loadConfig        func(string) (sessionmove.Config, error)
	resolveSource     func() (session.Record, bool, error)
	checkpoint        func(context.Context, worktrees.SessionCheckpointOptions) (worktrees.SessionCheckpointResult, error)
	store             func(string) (sessionmove.Store, error)
	newDeliverer      func(sessionmove.TargetConfig, sessionmove.Courier, sessioncourier.SynchestraOptions) (sessioncourier.Deliverer, error)
	acknowledge       func(context.Context, sessioncustody.Options) (sessioncustody.Result, error)
}

func defaultSessionMoveDependencies() sessionMoveDependencies {
	return sessionMoveDependencies{
		defaultConfigPath: wbconfig.DefaultPath,
		loadConfig:        sessionmove.LoadConfig,
		resolveSource: func() (session.Record, bool, error) {
			directory, err := sessionDirForRead()
			if err != nil {
				return session.Record{}, false, err
			}
			record, ok := session.ResolveForProcess(directory, os.Getpid())
			return record, ok, nil
		},
		checkpoint: worktrees.CreateSessionCheckpoint,
		store: func(root string) (sessionmove.Store, error) {
			home, err := wbhome.Root(root)
			if err != nil {
				return sessionmove.Store{}, err
			}
			return sessionmove.NewStore(filepath.Join(home, sessionmove.DirName)), nil
		},
		newDeliverer: func(target sessionmove.TargetConfig, courier sessionmove.Courier, options sessioncourier.SynchestraOptions) (sessioncourier.Deliverer, error) {
			switch courier {
			case sessionmove.CourierSSH:
				if target.SSH == nil {
					return nil, fmt.Errorf("target %q has no ssh courier configured", target.Machine)
				}
				return sessioncourier.NewSSHDeliverer(*target.SSH)
			case sessionmove.CourierSynchestra:
				if target.Synchestra == nil {
					return nil, fmt.Errorf("target %q has no synchestra courier configured", target.Machine)
				}
				return sessioncourier.NewSynchestraDeliverer(*target.Synchestra, options)
			default:
				return nil, fmt.Errorf("courier %q is not implemented by this WB build", courier)
			}
		},
		acknowledge: sessioncustody.Acknowledge,
	}
}

type sessionMoveOutput struct {
	Phase        string                        `json:"phase"`
	Courier      sessionmove.Courier           `json:"courier"`
	SourceActive bool                          `json:"source_active"`
	Request      sessionmove.Request           `json:"request"`
	Digest       sessionmove.Digest            `json:"request_digest"`
	Successor    *sessionlaunch.Result         `json:"successor,omitempty"`
	Receipt      *sessionmove.Receipt          `json:"receipt,omitempty"`
	Address      *sessionmove.SuccessorAddress `json:"successor_address,omitempty"`
}

func newSessionMoveCmd() *cobra.Command {
	return newSessionMoveCmdWithDeps(defaultSessionMoveDependencies())
}

func newSessionMoveCmdWithDeps(deps sessionMoveDependencies) *cobra.Command {
	var targetMachine, via, configPath, handoverFile, harness, resume string
	var summary, validation, remaining, format string
	command := &cobra.Command{
		Use:   "move [worktree-path]",
		Short: "Move this registered session through an exact receipt-gated handoff",
		Long: `Create the source-owned checkpoint for a portable session move.

WB requires a live registered session, an active managed Work Log, a clean
named branch, and a non-empty handover supplied from a file or stdin. It
generates and commits only .wb/handoffs/<id>.md, performs a normal non-force
push, verifies that exact commit as the remote branch tip, and records an
offer without transferring source custody. It then delivers the exact request
through the selected immutable courier route and starts the successor in a
named tmux session. Only a durable target receipt lets WB publish the stable
successor address and seal predecessor custody. If delivery or acknowledgement
is ambiguous, retry the same handoff with --resume; WB repairs the exact
aggregate and never creates a second checkpoint or successor for that retry.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			resume = strings.TrimSpace(resume)
			if resume != "" {
				return runSessionMoveResume(command, deps, resume, via, configPath, format, args)
			}
			targetMachine = strings.TrimSpace(targetMachine)
			if targetMachine == "" {
				return fmt.Errorf("--to is required")
			}
			if strings.TrimSpace(configPath) == "" {
				configPath = deps.defaultConfigPath()
			}
			config, err := deps.loadConfig(configPath)
			if err != nil {
				return err
			}
			target, ok := config.Target(targetMachine)
			if !ok {
				return fmt.Errorf("session move target %q is not configured in %s", targetMachine, configPath)
			}
			courier, err := selectSessionMoveCourier(target, via)
			if err != nil {
				return err
			}
			var deliveryStore sessionmove.Store
			storeReady := false
			freshOptions := sessioncourier.SynchestraOptions{}
			if courier == sessionmove.CourierSynchestra {
				freshOptions.SaveDispatch = func(identity sessionmove.SynchestraDispatch) error {
					if !storeReady {
						return errors.New("durable handoff store is unavailable before Synchestra delivery")
					}
					_, _, saveErr := deliveryStore.SaveSynchestraDispatch(identity)
					return saveErr
				}
			}
			deliverer, err := deps.newDeliverer(target, courier, freshOptions)
			if err != nil {
				return err
			}
			source, ok, err := deps.resolveSource()
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("session move requires a live registered source session that owns this process; run wb session register at session start")
			}
			if err := sessionlaunch.ValidateHarnessSelection(source.Runtime, harness); err != nil {
				return err
			}
			body, err := readSessionHandover(command, handoverFile)
			if err != nil {
				return err
			}
			result, err := deps.checkpoint(command.Context(), worktrees.SessionCheckpointOptions{
				ProjectsRoot:     projectsRoot,
				Worktree:         argumentOrCurrent(args),
				SourceSession:    source,
				TargetMachine:    targetMachine,
				RequestedHarness: harness,
				Handover: worktrees.SessionHandover{
					Summary: summary, ValidationEvidence: validation, RemainingWork: remaining, Body: body,
				},
			})
			if err != nil {
				if result.Request.HandoffID != "" {
					return resumablePostCheckpointError(result.Request.HandoffID, "finish source checkpoint evidence", err)
				}
				return err
			}
			deliveryStore, err = deps.store(projectsRoot)
			if err != nil {
				return resumablePostCheckpointError(result.Request.HandoffID, "open durable move state", err)
			}
			storeReady = true
			route := sessionMoveRoute(result.Request, result.Digest, courier, target)
			if _, _, err := deliveryStore.SaveRoute(route); err != nil {
				return resumablePostCheckpointError(result.Request.HandoffID, "persist immutable courier route", err)
			}
			delivery, err := deliverer.Deliver(command.Context(), result.RequestBytes)
			if err != nil {
				return resumableDeliveryError(result.Request.HandoffID, err)
			}
			output, err := acknowledgeSessionMove(command.Context(), deps, deliveryStore, source, courier,
				result.Request, result.Digest, delivery)
			if err != nil {
				return resumablePostCheckpointError(result.Request.HandoffID, "complete receipt-gated source custody", err)
			}
			if format == "json" {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(output)
			}
			_, err = fmt.Fprintf(command.OutOrStdout(),
				"moved session %s to successor %s for handoff %s on %s via %s in tmux %s at exact commit %s; predecessor custody is sealed\n",
				result.Request.PredecessorWBSessionID, output.Receipt.SuccessorWBSessionID,
				result.Request.HandoffID, result.Request.TargetMachine, courier, output.Receipt.TmuxName,
				result.Request.BundleCommit,
			)
			return err
		},
	}
	command.Flags().StringVar(&targetMachine, "to", "", "configured target WB machine (required)")
	command.Flags().StringVar(&via, "via", "", "configured courier: ssh or synchestra (default: target default_courier)")
	command.Flags().StringVar(&configPath, "config", "", "path to wb.yaml (default: ~/.config/wb/wb.yaml)")
	command.Flags().StringVar(&handoverFile, "handover-file", "", "agent-authored handover file, or - for stdin (required)")
	command.Flags().StringVar(&summary, "summary", "", "handover summary recorded in the tracked document and Work Log")
	command.Flags().StringVar(&validation, "validation", "", "validation evidence recorded in the tracked document")
	command.Flags().StringVar(&remaining, "remaining", "", "remaining work and next action recorded in the tracked document")
	command.Flags().StringVar(&harness, "harness", "", "requested successor harness (default: source runtime)")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().StringVar(&resume, "resume", "", "retry the exact immutable courier route for an existing handoff ID")
	return command
}

func runSessionMoveResume(command *cobra.Command, deps sessionMoveDependencies, handoffID, via, configPath, format string, args []string) error {
	if len(args) != 0 || command.Flags().Changed("handover-file") || command.Flags().Changed("harness") || command.Flags().Changed("summary") ||
		command.Flags().Changed("validation") || command.Flags().Changed("remaining") || command.Flags().Changed("to") {
		return fmt.Errorf("--resume accepts only an existing handoff ID plus optional --via, --config, and --format")
	}
	source, ok, err := deps.resolveSource()
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("session move resume requires the live registered predecessor session")
	}
	store, err := deps.store(projectsRoot)
	if err != nil {
		return err
	}
	request, digest, raw, err := store.RequestBytes(handoffID)
	if err != nil {
		return err
	}
	if request.PredecessorWBSessionID != source.WBSessionID {
		return fmt.Errorf("handoff %s belongs to predecessor session %s", handoffID, request.PredecessorWBSessionID)
	}
	route, routeErr := store.LoadRoute(handoffID)
	if routeErr != nil {
		if !errors.Is(routeErr, os.ErrNotExist) {
			return routeErr
		}
		if strings.TrimSpace(configPath) == "" {
			configPath = deps.defaultConfigPath()
		}
		config, loadErr := deps.loadConfig(configPath)
		if loadErr != nil {
			return loadErr
		}
		target, found := config.Target(request.TargetMachine)
		if !found {
			return fmt.Errorf("session move target %q is not configured", request.TargetMachine)
		}
		courier, selectErr := selectSessionMoveCourier(target, via)
		if selectErr != nil {
			return selectErr
		}
		route = sessionMoveRoute(request, digest, courier, target)
		if _, _, saveErr := store.SaveRoute(route); saveErr != nil {
			return saveErr
		}
	}
	if strings.TrimSpace(via) != "" && sessionmove.Courier(strings.TrimSpace(via)) != route.Courier {
		return fmt.Errorf("--via cannot change immutable courier route %q", route.Courier)
	}
	target := sessionmove.TargetConfig{Machine: route.TargetMachine, DefaultCourier: route.Courier, SSH: route.SSH, Synchestra: route.Synchestra}
	state, err := store.Load(handoffID)
	if err != nil {
		return err
	}
	var delivery sessionreceive.Result
	if state.Receipt != nil {
		receipt := *state.Receipt
		delivery = sessionreceive.Result{Request: request, Digest: digest, Phase: sessionmove.PhaseCompleted,
			Receipt: &receipt, Replay: true}
	} else {
		options, optionsErr := sessionMoveSynchestraOptions(store, handoffID, route.Courier)
		if optionsErr != nil {
			return optionsErr
		}
		deliverer, delivererErr := deps.newDeliverer(target, route.Courier, options)
		if delivererErr != nil {
			return delivererErr
		}
		delivery, err = deliverer.Deliver(command.Context(), raw)
		if err != nil {
			return resumableDeliveryError(handoffID, err)
		}
	}
	output, err := acknowledgeSessionMove(command.Context(), deps, store, source, route.Courier, request, digest, delivery)
	if err != nil {
		return resumablePostCheckpointError(handoffID, "complete receipt-gated source custody", err)
	}
	if format == "json" {
		encoder := json.NewEncoder(command.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(output)
	}
	_, err = fmt.Fprintf(command.OutOrStdout(), "completed handoff %s to successor %s via %s in tmux %s; predecessor custody is sealed\n",
		handoffID, output.Receipt.SuccessorWBSessionID, route.Courier, output.Receipt.TmuxName)
	return err
}

func sessionMoveRoute(request sessionmove.Request, digest sessionmove.Digest, courier sessionmove.Courier, target sessionmove.TargetConfig) sessionmove.Route {
	route := sessionmove.Route{
		HandoffID: request.HandoffID, RequestDigest: digest,
		TargetMachine: request.TargetMachine, Courier: courier,
	}
	switch courier {
	case sessionmove.CourierSSH:
		route.SSH = target.SSH
	case sessionmove.CourierSynchestra:
		route.Synchestra = target.Synchestra
	}
	return route
}

func sessionMoveSynchestraOptions(store sessionmove.Store, handoffID string, courier sessionmove.Courier) (sessioncourier.SynchestraOptions, error) {
	if courier != sessionmove.CourierSynchestra {
		return sessioncourier.SynchestraOptions{}, nil
	}
	options := sessioncourier.SynchestraOptions{
		SaveDispatch: func(identity sessionmove.SynchestraDispatch) error {
			_, _, err := store.SaveSynchestraDispatch(identity)
			return err
		},
	}
	dispatch, err := store.LoadSynchestraDispatch(handoffID)
	if err == nil {
		options.Dispatch = &dispatch
		return options, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return options, nil
	}
	return sessioncourier.SynchestraOptions{}, err
}

func acknowledgeSessionMove(
	ctx context.Context,
	deps sessionMoveDependencies,
	store sessionmove.Store,
	source session.Record,
	courier sessionmove.Courier,
	request sessionmove.Request,
	digest sessionmove.Digest,
	delivery sessionreceive.Result,
) (sessionMoveOutput, error) {
	var output sessionMoveOutput
	if delivery.Phase != sessionmove.PhaseCompleted || delivery.Receipt == nil {
		return output, errors.New("courier returned no durable completion receipt")
	}
	deliveredRequest, err := sessionmove.EncodeRequest(delivery.Request)
	if err != nil {
		return output, fmt.Errorf("validate courier response request: %w", err)
	}
	expectedRequest, err := sessionmove.EncodeRequest(request)
	if err != nil {
		return output, err
	}
	if !bytes.Equal(deliveredRequest, expectedRequest) || delivery.Digest != digest {
		return output, fmt.Errorf("courier completion does not match the exact delivered request")
	}
	if err := sessionmove.ValidateReceiptForRequest(*delivery.Receipt, request, digest); err != nil {
		return output, fmt.Errorf("validate target completion receipt: %w", err)
	}
	acknowledge := deps.acknowledge
	if acknowledge == nil {
		return output, errors.New("source custody acknowledger is unavailable")
	}
	acknowledged, err := acknowledge(ctx, sessioncustody.Options{
		Store: store, ProjectsRoot: projectsRoot, Request: request, RequestDigest: digest,
		Receipt: *delivery.Receipt, SourceSession: source,
	})
	if err != nil {
		return output, err
	}
	receipt := acknowledged.Receipt
	address := acknowledged.Address
	return sessionMoveOutput{
		Phase: string(sessionmove.PhaseCompleted), Courier: courier, SourceActive: false,
		Request: request, Digest: digest, Successor: delivery.Successor, Receipt: &receipt, Address: &address,
	}, nil
}

func resumableDeliveryError(handoffID string, err error) error {
	return fmt.Errorf("delivery for handoff %s failed or is ambiguous; retry the exact route with `wb session move --resume %s`: %w", handoffID, handoffID, err)
}

func resumablePostCheckpointError(handoffID, operation string, err error) error {
	return fmt.Errorf("handoff %s is durably checkpointed but WB could not %s; retry the same handoff with `wb session move --resume %s`: %w",
		handoffID, operation, handoffID, err)
}

func selectSessionMoveCourier(target sessionmove.TargetConfig, requested string) (sessionmove.Courier, error) {
	courier := target.DefaultCourier
	if requested = strings.TrimSpace(requested); requested != "" {
		courier = sessionmove.Courier(requested)
	}
	switch courier {
	case sessionmove.CourierSSH:
		if target.SSH == nil {
			return "", fmt.Errorf("target %q has no ssh courier configured", target.Machine)
		}
	case sessionmove.CourierSynchestra:
		if target.Synchestra == nil {
			return "", fmt.Errorf("target %q has no synchestra courier configured", target.Machine)
		}
	default:
		return "", fmt.Errorf("--via must be %q or %q", sessionmove.CourierSSH, sessionmove.CourierSynchestra)
	}
	return courier, nil
}

func readSessionHandover(command *cobra.Command, path string) ([]byte, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("--handover-file is required; use - to read stdin")
	}
	var reader io.Reader
	var file *os.File
	if path == "-" {
		reader = command.InOrStdin()
	} else {
		var err error
		file, err = os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open handover file %s: %w", path, err)
		}
		defer func() { _ = file.Close() }()
		info, err := file.Stat()
		if err != nil {
			return nil, fmt.Errorf("inspect handover file %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("handover file %s must be a regular file", path)
		}
		reader = file
	}
	contents, err := io.ReadAll(io.LimitReader(reader, maxSessionHandoverBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read handover: %w", err)
	}
	if len(contents) > maxSessionHandoverBytes {
		return nil, fmt.Errorf("handover exceeds %d bytes", maxSessionHandoverBytes)
	}
	if len(bytes.TrimSpace(contents)) == 0 {
		return nil, fmt.Errorf("handover must not be empty")
	}
	return contents, nil
}
