package sessionrun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/sneat-dev/wb/internal/continuationinput"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/secretscan"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessioncourier"
	"github.com/sneat-dev/wb/internal/sessioncustody"
	"github.com/sneat-dev/wb/internal/sessionlaunch"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionreceive"
	"github.com/sneat-dev/wb/internal/wbconfig"
	"github.com/sneat-dev/wb/internal/worktrees"
	"os"
	"strings"
)

const maxSessionHandoverBytes = 1 << 20

type MoveDependencies struct {
	LoadScanner       func() (*secretscan.Scanner, []string, error)
	DefaultConfigPath func() string
	LoadConfig        func(string) (sessionmove.Config, error)
	LocalMachine      func() (string, error)
	ResolveSource     func(string) (session.Record, bool, error)
	Checkpoint        func(context.Context, worktrees.SessionCheckpointOptions) (worktrees.SessionCheckpointResult, error)
	Store             func(string) (sessionmove.Store, error)
	NewDeliverer      func(sessionmove.TargetConfig, sessionmove.Courier, sessioncourier.SynchestraOptions) (sessioncourier.Deliverer, error)
	LoopbackDeliverer func(sessionmove.Store) sessioncourier.Deliverer
	Acknowledge       func(context.Context, sessioncustody.Options) (sessioncustody.Result, error)
}

func DefaultMoveDependencies() MoveDependencies {
	return MoveDependencies{LoadScanner: DefaultScanner,
		DefaultConfigPath: wbconfig.DefaultPath,
		LoadConfig:        sessionmove.LoadConfig,
		LocalMachine: func() (string, error) {
			config, err := remotestate.LoadConfig(wbconfig.DefaultPath())
			if err != nil {
				return "", err
			}
			return strings.TrimSpace(config.Machine), nil
		},
		ResolveSource: ResolveSource,
		Checkpoint:    worktrees.CreateSessionCheckpoint,
		Store:         MoveStore,
		NewDeliverer: func(target sessionmove.TargetConfig, courier sessionmove.Courier, options sessioncourier.SynchestraOptions) (sessioncourier.Deliverer, error) {
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
		Acknowledge: sessioncustody.Acknowledge,
	}
}

type MoveService struct{ deps MoveDependencies }

func NewMove(deps MoveDependencies) *MoveService { return &MoveService{deps: deps} }
func (s *MoveService) Move(ctx context.Context, request MoveRequest, warn func([]secretscan.Finding)) (MoveResult, error) {
	projectsRoot, targetMachine, via, configPath := request.ProjectsRoot, request.Target, request.Via, request.ConfigPath
	handoverFile, harness, model, resume := request.HandoverFile, request.Harness, request.Model, request.ResumeID
	summary, validation, remaining, overrideSecrets := request.Summary, request.Validation, request.Remaining, request.OverrideSecrets
	deps := s.deps
	resume = strings.TrimSpace(resume)
	if resume != "" {
		return s.Resume(ctx, request)
	}
	targetMachine = strings.TrimSpace(targetMachine)
	LocalMachine := ""
	if deps.LocalMachine != nil {
		machine, machineErr := deps.LocalMachine()
		if machineErr != nil && targetMachine == "" {
			return MoveResult{}, fmt.Errorf("resolve this machine for a local move: %w", machineErr)
		}
		if machineErr == nil {
			LocalMachine = strings.TrimSpace(machine)
		}
	}
	if targetMachine == "" {
		if LocalMachine == "" {
			return MoveResult{}, fmt.Errorf("--to is required when this machine has no validated remote.machine")
		}
		targetMachine = LocalMachine
	}
	useLoopback := LocalMachine != "" && targetMachine == LocalMachine
	if strings.TrimSpace(configPath) == "" {
		configPath = deps.DefaultConfigPath()
	}
	var target sessionmove.TargetConfig
	var courier sessionmove.Courier
	if useLoopback {
		if via != "" && sessionmove.Courier(strings.TrimSpace(via)) != sessionmove.CourierLoopback {
			return MoveResult{}, fmt.Errorf("local move on this machine uses the loopback courier; omit --via or pass --via loopback")
		}
		courier = sessionmove.CourierLoopback
	} else {
		config, err := deps.LoadConfig(configPath)
		if err != nil {
			return MoveResult{}, err
		}
		var ok bool
		target, ok = config.Target(targetMachine)
		if !ok {
			return MoveResult{}, fmt.Errorf("session move target %q is not configured in %s", targetMachine, configPath)
		}
		courier, err = selectMoveCourier(target, via)
		if err != nil {
			return MoveResult{}, err
		}
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
	var deliverer sessioncourier.Deliverer
	var err error
	if courier != sessionmove.CourierLoopback {
		deliverer, err = deps.NewDeliverer(target, courier, freshOptions)
		if err != nil {
			return MoveResult{}, err
		}
	}
	source, ok, err := deps.ResolveSource(projectsRoot)
	if err != nil {
		return MoveResult{}, err
	}
	if !ok {
		return MoveResult{}, fmt.Errorf("session move requires a live registered source session that owns this process; run wb session register at session start")
	}
	normalizedHarness, err := sessionlaunch.NormalizeRuntime(source.Runtime, harness)
	if err != nil {
		return MoveResult{}, err
	}
	if strings.TrimSpace(harness) == "" {
		normalizedHarness = ""
	}
	model = sessionlaunch.NormalizeModel(model)
	body, err := continuationinput.ReadHandover(request.Input, handoverFile, maxSessionHandoverBytes)
	if err != nil {
		return MoveResult{}, err
	}
	overrides, err := secretscan.ParseOverrides(overrideSecrets)
	if err != nil {
		return MoveResult{}, err
	}
	secretWarnings, err := ScanContinuation(deps.LoadScanner, overrides,
		secretscan.Segment{Name: "summary", Content: []byte(summary)},
		secretscan.Segment{Name: "validation", Content: []byte(validation)},
		secretscan.Segment{Name: "remaining", Content: []byte(remaining)},
		secretscan.Segment{Name: "handover-body", Content: body},
	)
	if err != nil {
		return MoveResult{}, err
	}
	if warn != nil {
		warn(secretWarnings)
	}
	result, err := deps.Checkpoint(ctx, worktrees.SessionCheckpointOptions{
		ProjectsRoot:     projectsRoot,
		Worktree:         request.Worktree,
		SourceSession:    source,
		TargetMachine:    targetMachine,
		RequestedHarness: normalizedHarness,
		RequestedModel:   model,
		Handover: worktrees.SessionHandover{
			Summary: summary, ValidationEvidence: validation, RemainingWork: remaining, Body: body,
		},
	})
	if err != nil {
		if result.Request.HandoffID != "" {
			return MoveResult{}, resumablePostCheckpointError(result.Request.HandoffID, "finish source checkpoint evidence", err)
		}
		return MoveResult{}, err
	}
	deliveryStore, err = deps.Store(projectsRoot)
	if err != nil {
		return MoveResult{}, resumablePostCheckpointError(result.Request.HandoffID, "open durable move state", err)
	}
	storeReady = true
	if courier == sessionmove.CourierLoopback {
		if deps.LoopbackDeliverer != nil {
			deliverer = deps.LoopbackDeliverer(deliveryStore)
		} else {
			deliverer = sessioncourier.LoopbackDeliverer{LocalMachine: LocalMachine, ProjectsRoot: projectsRoot, Store: deliveryStore}
		}
	}
	if deliverer == nil {
		return MoveResult{}, fmt.Errorf("session move courier %q is not configured", courier)
	}
	route := moveRoute(result.Request, result.Digest, courier, target)
	if _, _, err := deliveryStore.SaveRoute(route); err != nil {
		return MoveResult{}, resumablePostCheckpointError(result.Request.HandoffID, "persist immutable courier route", err)
	}
	delivery, err := deliverer.Deliver(ctx, result.RequestBytes)
	if err != nil {
		return MoveResult{}, resumableDeliveryError(result.Request.HandoffID, err)
	}
	output, err := acknowledgeMove(projectsRoot, ctx, deps, deliveryStore, source, courier,
		result.Request, result.Digest, delivery)
	if err != nil {
		return MoveResult{}, resumablePostCheckpointError(result.Request.HandoffID, "complete receipt-gated source custody", err)
	}
	return output, nil
}
func (s *MoveService) Resume(ctx context.Context, options MoveRequest) (MoveResult, error) {
	deps := s.deps
	projectsRoot, handoffID, via, configPath := options.ProjectsRoot, options.ResumeID, options.Via, options.ConfigPath
	source, ok, err := deps.ResolveSource(projectsRoot)
	if err != nil {
		return MoveResult{}, err
	}
	if !ok {
		return MoveResult{}, fmt.Errorf("session move resume requires the live registered predecessor session")
	}
	Store, err := deps.Store(projectsRoot)
	if err != nil {
		return MoveResult{}, err
	}
	request, digest, raw, err := Store.RequestBytes(handoffID)
	if err != nil {
		return MoveResult{}, err
	}
	if request.PredecessorWBSessionID != source.WBSessionID {
		return MoveResult{}, fmt.Errorf("handoff %s belongs to predecessor session %s", handoffID, request.PredecessorWBSessionID)
	}
	route, routeErr := Store.LoadRoute(handoffID)
	if routeErr != nil {
		if !errors.Is(routeErr, os.ErrNotExist) {
			return MoveResult{}, routeErr
		}
		if strings.TrimSpace(configPath) == "" {
			configPath = deps.DefaultConfigPath()
		}
		config, loadErr := deps.LoadConfig(configPath)
		if loadErr != nil {
			return MoveResult{}, loadErr
		}
		target, found := config.Target(request.TargetMachine)
		if !found {
			return MoveResult{}, fmt.Errorf("session move target %q is not configured", request.TargetMachine)
		}
		courier, selectErr := selectMoveCourier(target, via)
		if selectErr != nil {
			return MoveResult{}, selectErr
		}
		route = moveRoute(request, digest, courier, target)
		if _, _, saveErr := Store.SaveRoute(route); saveErr != nil {
			return MoveResult{}, saveErr
		}
	}
	if strings.TrimSpace(via) != "" && sessionmove.Courier(strings.TrimSpace(via)) != route.Courier {
		return MoveResult{}, fmt.Errorf("--via cannot change immutable courier route %q", route.Courier)
	}
	target := sessionmove.TargetConfig{Machine: route.TargetMachine, DefaultCourier: route.Courier, SSH: route.SSH, Synchestra: route.Synchestra}
	state, err := Store.Load(handoffID)
	if err != nil {
		return MoveResult{}, err
	}
	var delivery sessionreceive.Result
	if state.Receipt != nil {
		receipt := *state.Receipt
		delivery = sessionreceive.Result{Request: request, Digest: digest, Phase: sessionmove.PhaseCompleted,
			Receipt: &receipt, Replay: true}
	} else {
		options, optionsErr := moveSynchestraOptions(Store, handoffID, route.Courier)
		if optionsErr != nil {
			return MoveResult{}, optionsErr
		}
		var deliverer sessioncourier.Deliverer
		var delivererErr error
		if route.Courier == sessionmove.CourierLoopback {
			if deps.LoopbackDeliverer != nil {
				deliverer = deps.LoopbackDeliverer(Store)
			} else {
				LocalMachine := route.TargetMachine
				if deps.LocalMachine != nil {
					if machine, machineErr := deps.LocalMachine(); machineErr == nil {
						LocalMachine = strings.TrimSpace(machine)
					}
				}
				deliverer = sessioncourier.LoopbackDeliverer{LocalMachine: LocalMachine, ProjectsRoot: projectsRoot, Store: Store}
			}
		} else {
			deliverer, delivererErr = deps.NewDeliverer(target, route.Courier, options)
		}
		if delivererErr != nil {
			return MoveResult{}, delivererErr
		}
		delivery, err = deliverer.Deliver(ctx, raw)
		if err != nil {
			return MoveResult{}, resumableDeliveryError(handoffID, err)
		}
	}
	output, err := acknowledgeMove(projectsRoot, ctx, deps, Store, source, route.Courier, request, digest, delivery)
	if err != nil {
		return MoveResult{}, resumablePostCheckpointError(handoffID, "complete receipt-gated source custody", err)
	}
	output.Resume = true
	return output, nil
}
func moveRoute(request sessionmove.Request, digest sessionmove.Digest, courier sessionmove.Courier, target sessionmove.TargetConfig) sessionmove.Route {
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
func moveSynchestraOptions(Store sessionmove.Store, handoffID string, courier sessionmove.Courier) (sessioncourier.SynchestraOptions, error) {
	if courier != sessionmove.CourierSynchestra {
		return sessioncourier.SynchestraOptions{}, nil
	}
	options := sessioncourier.SynchestraOptions{
		SaveDispatch: func(identity sessionmove.SynchestraDispatch) error {
			_, _, err := Store.SaveSynchestraDispatch(identity)
			return err
		},
	}
	dispatch, err := Store.LoadSynchestraDispatch(handoffID)
	if err == nil {
		options.Dispatch = &dispatch
		return options, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return options, nil
	}
	return sessioncourier.SynchestraOptions{}, err
}
func acknowledgeMove(projectsRoot string, ctx context.Context,
	deps MoveDependencies,
	Store sessionmove.Store,
	source session.Record,
	courier sessionmove.Courier,
	request sessionmove.Request,
	digest sessionmove.Digest,
	delivery sessionreceive.Result) (MoveResult, error) {
	var output MoveResult
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
	Acknowledge := deps.Acknowledge
	if Acknowledge == nil {
		return output, errors.New("source custody acknowledger is unavailable")
	}
	acknowledged, err := Acknowledge(ctx, sessioncustody.Options{
		Store: Store, ProjectsRoot: projectsRoot, Request: request, RequestDigest: digest,
		Receipt: *delivery.Receipt, SourceSession: source,
	})
	if err != nil {
		return output, err
	}
	receipt := acknowledged.Receipt
	address := acknowledged.Address
	return MoveResult{
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
func selectMoveCourier(target sessionmove.TargetConfig, requested string) (sessionmove.Courier, error) {
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
