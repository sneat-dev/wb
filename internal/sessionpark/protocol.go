package sessionpark

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sneat-dev/wb/internal/gitremote"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionauthority"
	"github.com/sneat-dev/wb/internal/sessionmove"
)

const (
	EnvelopeSchemaVersion    = 1
	RequestSchemaVersion     = 1
	ReceiptSchemaVersion     = 1
	EnvelopeKind             = "session_park_resume"
	MaxEnvelopeBytes         = 1 << 20
	MaxMembers               = 32
	MaxFieldBytes            = 4096
	TargetDirName            = "park-resumes"
	EnvelopeFileName         = "envelope.json"
	ContinuationFileName     = "continuation.md"
	SuccessorContextFileName = "successor-context.md"
	MaxSuccessorContextBytes = 256 << 10
)

type Envelope struct {
	SchemaVersion int           `json:"schema_version"`
	Kind          string        `json:"kind"`
	Request       RemoteRequest `json:"request"`
}

// RemoteRequest is independent of sessionmove.Request schema v1. It carries
// one parked aggregate and every exact member needed for an atomic target
// reconstruction.
type RemoteRequest struct {
	SchemaVersion          int            `json:"schema_version"`
	ResumeID               string         `json:"resume_id"`
	ParkedSessionID        string         `json:"parked_session_id"`
	SuccessorWBSessionID   string         `json:"successor_wb_session_id"`
	PredecessorWBSessionID string         `json:"predecessor_wb_session_id"`
	SourceMachine          string         `json:"source_machine"`
	TargetMachine          string         `json:"target_machine"`
	SourceRuntime          string         `json:"source_runtime"`
	SourceModel            string         `json:"source_model,omitempty"`
	RequestedHarness       string         `json:"requested_harness,omitempty"`
	Continuation           string         `json:"continuation"`
	Members                []RemoteMember `json:"members"`
	CreatedAt              time.Time      `json:"created_at"`
}

type RemoteMember struct {
	MemberID               string `json:"member_id"`
	Repository             string `json:"repository"`
	RepositoryRemote       string `json:"repository_remote"`
	Branch                 string `json:"branch"`
	Commit                 string `json:"commit"`
	SourceWorkLogReference string `json:"source_work_log_reference"`
}

type Receipt struct {
	SchemaVersion          int                `json:"schema_version"`
	ResumeID               string             `json:"resume_id"`
	RequestDigest          sessionmove.Digest `json:"request_digest"`
	ParkedSessionID        string             `json:"parked_session_id"`
	SuccessorWBSessionID   string             `json:"successor_wb_session_id"`
	PredecessorWBSessionID string             `json:"predecessor_wb_session_id"`
	TargetMachine          string             `json:"target_machine"`
	TmuxName               string             `json:"tmux_name"`
	Runtime                string             `json:"runtime"`
	Model                  string             `json:"model,omitempty"`
	NativeHarnessID        string             `json:"native_harness_id,omitempty"`
	AttemptID              string             `json:"attempt_id"`
	AttemptIndex           uint64             `json:"attempt_index"`
	PID                    int                `json:"pid"`
	StartedAt              time.Time          `json:"started_at"`
	Members                []ReceiptMember    `json:"members"`
}

type ReceiptMember struct {
	MemberID               string `json:"member_id"`
	Repository             string `json:"repository"`
	TargetPath             string `json:"target_path"`
	Pin                    string `json:"pin"`
	Commit                 string `json:"commit"`
	TargetWorkLogReference string `json:"target_work_log_reference"`
}

var gitObjectID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
var attemptID = regexp.MustCompile(`^[0-9]{6}-[0-9a-f]{32}$`)

func EncodeEnvelope(envelope Envelope) ([]byte, error) {
	if err := validateEnvelope(envelope); err != nil {
		return nil, err
	}
	raw, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return nil, err
	}
	raw = append(raw, '\n')
	if len(raw) > MaxEnvelopeBytes {
		return nil, fmt.Errorf("park resume envelope exceeds %d bytes", MaxEnvelopeBytes)
	}
	return raw, nil
}

func DecodeEnvelope(raw []byte) (Envelope, error) {
	var envelope Envelope
	if len(raw) == 0 || len(raw) > MaxEnvelopeBytes {
		return envelope, fmt.Errorf("park resume envelope must be non-empty and at most %d bytes", MaxEnvelopeBytes)
	}
	if err := strictDecode(raw, &envelope); err != nil {
		return envelope, fmt.Errorf("parse park resume envelope: %w", err)
	}
	if err := validateEnvelope(envelope); err != nil {
		return envelope, err
	}
	return envelope, nil
}

func EncodeReceipt(receipt Receipt) ([]byte, error) {
	if err := validateReceiptShape(receipt); err != nil {
		return nil, err
	}
	raw, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

func DecodeReceipt(raw []byte) (Receipt, error) {
	var receipt Receipt
	if len(raw) == 0 || len(raw) > MaxEnvelopeBytes {
		return receipt, fmt.Errorf("park resume receipt must be non-empty and bounded")
	}
	if err := strictDecode(raw, &receipt); err != nil {
		return receipt, fmt.Errorf("parse park resume receipt: %w", err)
	}
	if err := validateReceiptShape(receipt); err != nil {
		return receipt, err
	}
	return receipt, nil
}

func ValidateReceipt(receipt Receipt, request RemoteRequest, digest sessionmove.Digest) error {
	if err := validateRequest(request); err != nil {
		return err
	}
	if err := validateReceiptShape(receipt); err != nil {
		return err
	}
	if receipt.ResumeID != request.ResumeID || receipt.RequestDigest != digest ||
		receipt.ParkedSessionID != request.ParkedSessionID || receipt.SuccessorWBSessionID != request.SuccessorWBSessionID ||
		receipt.PredecessorWBSessionID != request.PredecessorWBSessionID || receipt.TargetMachine != request.TargetMachine ||
		receipt.TmuxName != "wb-session-"+request.SuccessorWBSessionID || len(receipt.Members) != len(request.Members) {
		return fmt.Errorf("park resume receipt conflicts with admitted request identity")
	}
	runtime, model := RequestedRuntimeModel(request)
	if receipt.Runtime != runtime || strings.TrimSpace(receipt.Model) != model {
		return fmt.Errorf("park resume receipt harness identity conflicts with admitted request")
	}
	for index, member := range request.Members {
		got := receipt.Members[index]
		if got.MemberID != member.MemberID || got.Repository != member.Repository || got.Commit != member.Commit ||
			got.Pin != MemberPin(request.ResumeID, member.MemberID) {
			return fmt.Errorf("park resume receipt member %d conflicts with admitted request", index)
		}
		wantReference, err := TargetWorkLogReference(request, digest, member)
		if err != nil || got.TargetWorkLogReference != wantReference {
			return fmt.Errorf("park resume receipt member %s carries invalid target Work Log lineage", member.MemberID)
		}
	}
	return nil
}

func RequestedRuntimeModel(request RemoteRequest) (string, string) {
	runtime := strings.TrimSpace(request.RequestedHarness)
	if runtime == "" {
		runtime = strings.TrimSpace(request.SourceRuntime)
	}
	model := ""
	if runtime == strings.TrimSpace(request.SourceRuntime) {
		model = strings.TrimSpace(request.SourceModel)
	}
	return runtime, model
}

func MemberPin(resumeID, memberID string) string {
	return "wb-session/" + resumeID + "-" + memberID
}

func TargetWorkLogReference(request RemoteRequest, digest sessionmove.Digest, member RemoteMember) (string, error) {
	source, err := sessionmove.ParseWorkLogReference(member.SourceWorkLogReference)
	if err != nil {
		return "", err
	}
	claimID, err := TargetWorkLogClaimID(digest, request.SuccessorWBSessionID, member.MemberID, member.Repository, source.ClaimID)
	if err != nil {
		return "", err
	}
	return (sessionmove.WorkLogReference{EffortID: source.EffortID, RunID: source.RunID, ClaimID: claimID}).String(), nil
}

// TargetWorkLogClaimID binds one member's target claim to the exact parked
// envelope, successor, repository, and source claim. It is shared with the
// Work Log corroborator so the prepared claim is independently reproducible.
func TargetWorkLogClaimID(digest sessionmove.Digest, successorID, memberID, repository, sourceClaimID string) (string, error) {
	if !validDigest(digest) || !sessionauthority.ValidID(successorID) ||
		sessionauthority.ValidateMemberKey(memberID) != nil || strings.TrimSpace(repository) == "" ||
		len(repository) > MaxFieldBytes || strings.ContainsAny(repository, "\r\n") || !validSHA256Hex(sourceClaimID) {
		return "", fmt.Errorf("parked target Work Log claim identity is invalid")
	}
	hash := sha256.New()
	for _, part := range []string{"wb.session.park-target-claim.v1", string(digest), successorID, memberID, repository, sourceClaimID} {
		_, _ = hash.Write([]byte(fmt.Sprintf("%08x", len(part))))
		_, _ = hash.Write([]byte(part))
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func validDigest(digest sessionmove.Digest) bool {
	const prefix = sessionmove.DigestAlgorithmSHA256 + ":"
	value := string(digest)
	return strings.HasPrefix(value, prefix) && validSHA256Hex(strings.TrimPrefix(value, prefix))
}

func validSHA256Hex(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}

func LaunchAuthority(request RemoteRequest, digest sessionmove.Digest, continuationPath string, continuation []byte) (sessionauthority.Launch, error) {
	if err := validateRequest(request); err != nil {
		return sessionauthority.Launch{}, err
	}
	if len(continuation) == 0 || len(continuation) > MaxSuccessorContextBytes {
		return sessionauthority.Launch{}, fmt.Errorf("private successor context must be non-empty and at most %d bytes", MaxSuccessorContextBytes)
	}
	launch := sessionauthority.Launch{
		AggregateID: request.ResumeID, AggregateDigest: string(digest), AggregateFile: EnvelopeFileName,
		SuccessorWBSessionID: request.SuccessorWBSessionID, PredecessorWBSessionID: request.PredecessorWBSessionID,
		TargetMachine: request.TargetMachine, SourceRuntime: request.SourceRuntime, SourceModel: request.SourceModel,
		RequestedHarness: request.RequestedHarness, ContinuationKind: sessionauthority.ContinuationPrivate,
		ContinuationPath: continuationPath, ContinuationDigest: string(sessionmove.DigestBytes(continuation)),
		PinnedCommit: request.Members[0].Commit, PinnedBranch: MemberPin(request.ResumeID, request.Members[0].MemberID),
		RootMode: sessionauthority.LaunchRootPinnedClean,
	}
	return launch, launch.Validate()
}

func LocalLaunchAuthority(bundle Bundle, digest sessionmove.Digest, continuationPath string, continuation []byte) (sessionauthority.Launch, error) {
	if err := validateBundle(bundle); err != nil {
		return sessionauthority.Launch{}, err
	}
	if !validDigest(digest) || len(continuation) == 0 || len(continuation) > MaxSuccessorContextBytes ||
		!bytes.HasPrefix(continuation, []byte(bundle.Continuation)) {
		return sessionauthority.Launch{}, fmt.Errorf("private local successor context does not match the exact parked bundle")
	}
	seed := sha256.Sum256([]byte("wb.session.park-local.v1\x00" + bundle.ParkedSessionID))
	launch := sessionauthority.Launch{
		AggregateID: bundle.ParkedSessionID, AggregateDigest: string(digest), AggregateFile: BundleFileName,
		SuccessorWBSessionID: "wbs-" + hex.EncodeToString(seed[:16]), PredecessorWBSessionID: bundle.Source.WBSessionID,
		TargetMachine: bundle.Source.Machine, SourceRuntime: bundle.Source.Runtime, SourceModel: bundle.Source.Model,
		ContinuationKind: sessionauthority.ContinuationPrivate, ContinuationPath: continuationPath,
		ContinuationDigest: string(sessionmove.DigestBytes(continuation)), RootMode: sessionauthority.LaunchRootParkedNeutral,
	}
	if len(bundle.Worktrees) != 0 {
		launch.PinnedCommit = bundle.Worktrees[0].Head
		launch.PinnedBranch = bundle.Worktrees[0].Branch
		launch.RootMode = sessionauthority.LaunchRootParkedLocal
	}
	return launch, launch.Validate()
}

func validateEnvelope(envelope Envelope) error {
	if envelope.SchemaVersion != EnvelopeSchemaVersion {
		return fmt.Errorf("park resume envelope schema_version %d unsupported; want %d", envelope.SchemaVersion, EnvelopeSchemaVersion)
	}
	if envelope.Kind != EnvelopeKind {
		return fmt.Errorf("park resume envelope kind is invalid")
	}
	return validateRequest(envelope.Request)
}

func validateRequest(request RemoteRequest) error {
	if request.SchemaVersion != RequestSchemaVersion {
		return fmt.Errorf("park resume request schema_version %d unsupported; want %d", request.SchemaVersion, RequestSchemaVersion)
	}
	for name, value := range map[string]string{
		"resume_id": request.ResumeID, "parked_session_id": request.ParkedSessionID,
		"successor_wb_session_id": request.SuccessorWBSessionID, "predecessor_wb_session_id": request.PredecessorWBSessionID,
		"source_machine": request.SourceMachine, "target_machine": request.TargetMachine,
	} {
		if !sessionauthority.ValidID(value) {
			return fmt.Errorf("%s is not one fixed safe ID", name)
		}
	}
	if request.CreatedAt.IsZero() || strings.TrimSpace(request.SourceRuntime) == "" ||
		strings.ContainsAny(request.SourceRuntime+request.SourceModel+request.RequestedHarness, "\r\n") {
		return fmt.Errorf("park resume request source identity is incomplete")
	}
	if len(request.Continuation) == 0 || len([]byte(request.Continuation)) > MaxContinuationBytes || !utf8.ValidString(request.Continuation) {
		return fmt.Errorf("park resume continuation must be valid UTF-8 and between 1 and %d bytes", MaxContinuationBytes)
	}
	// Remote resume deliberately requires one designated launch member. Local
	// park/resume continues to support sessions owning zero worktrees.
	if len(request.Members) == 0 || len(request.Members) > MaxMembers {
		return fmt.Errorf("remote park resume requires between 1 and %d owned worktrees", MaxMembers)
	}
	seen := make(map[string]struct{}, len(request.Members))
	for index, member := range request.Members {
		if err := validateRemoteMember(member); err != nil {
			return fmt.Errorf("park resume member %d: %w", index, err)
		}
		if _, exists := seen[member.MemberID]; exists {
			return fmt.Errorf("park resume member ID %q is duplicated", member.MemberID)
		}
		seen[member.MemberID] = struct{}{}
	}
	return nil
}

func validateRemoteMember(member RemoteMember) error {
	if err := sessionauthority.ValidateMemberKey(member.MemberID); err != nil {
		return err
	}
	for name, value := range map[string]string{"repository": member.Repository, "branch": member.Branch, "source_work_log_reference": member.SourceWorkLogReference} {
		if strings.TrimSpace(value) == "" || len(value) > MaxFieldBytes || strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("%s is empty, oversized, or not single-line", name)
		}
	}
	remote, err := gitremote.Parse(member.RepositoryRemote)
	if err != nil {
		return fmt.Errorf("repository_remote is unsafe: %w", err)
	}
	if remote.Identity.Repository != member.Repository {
		return fmt.Errorf("repository does not match credential-free remote identity")
	}
	if !gitObjectID.MatchString(member.Commit) {
		return fmt.Errorf("commit must be one full lowercase Git object ID")
	}
	if _, err := sessionmove.ParseWorkLogReference(member.SourceWorkLogReference); err != nil {
		return fmt.Errorf("source Work Log reference: %w", err)
	}
	return nil
}

func validateReceiptShape(receipt Receipt) error {
	if receipt.SchemaVersion != ReceiptSchemaVersion {
		return fmt.Errorf("park resume receipt schema_version %d unsupported; want %d", receipt.SchemaVersion, ReceiptSchemaVersion)
	}
	for name, value := range map[string]string{
		"resume_id": receipt.ResumeID, "parked_session_id": receipt.ParkedSessionID,
		"successor_wb_session_id": receipt.SuccessorWBSessionID, "predecessor_wb_session_id": receipt.PredecessorWBSessionID,
		"target_machine": receipt.TargetMachine, "tmux_name": receipt.TmuxName,
	} {
		if !sessionauthority.ValidID(value) {
			return fmt.Errorf("receipt %s is not one fixed safe ID", name)
		}
	}
	if receipt.RequestDigest == "" || strings.TrimSpace(receipt.Runtime) == "" || !attemptID.MatchString(receipt.AttemptID) ||
		receipt.AttemptIndex == 0 || receipt.PID <= 0 || receipt.StartedAt.IsZero() || len(receipt.Members) == 0 || len(receipt.Members) > MaxMembers {
		return fmt.Errorf("park resume receipt successor identity is incomplete")
	}
	for _, member := range receipt.Members {
		if err := sessionauthority.ValidateMemberKey(member.MemberID); err != nil || member.Repository == "" ||
			!filepath.IsAbs(member.TargetPath) || filepath.Clean(member.TargetPath) != member.TargetPath || member.Pin == "" ||
			!gitObjectID.MatchString(member.Commit) {
			return fmt.Errorf("park resume receipt member is invalid")
		}
		if _, err := sessionmove.ParseWorkLogReference(member.TargetWorkLogReference); err != nil {
			return fmt.Errorf("park resume receipt target Work Log reference: %w", err)
		}
	}
	return nil
}

func strictDecode(raw []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}

func BuildRemoteRequest(bundle Bundle, target, requestedHarness string, now time.Time) RemoteRequest {
	seed := sha256.Sum256([]byte("wb.session.park-resume.v1\x00" + bundle.ParkedSessionID + "\x00" + target))
	request := RemoteRequest{
		SchemaVersion: RequestSchemaVersion, ResumeID: "resume-" + hex.EncodeToString(seed[:16]),
		ParkedSessionID: bundle.ParkedSessionID, SuccessorWBSessionID: "wbs-" + hex.EncodeToString(seed[16:]),
		PredecessorWBSessionID: bundle.Source.WBSessionID, SourceMachine: bundle.Source.Machine, TargetMachine: target,
		SourceRuntime: bundle.Source.Runtime, SourceModel: bundle.Source.Model, RequestedHarness: strings.TrimSpace(requestedHarness),
		Continuation: bundle.Continuation, CreatedAt: now.UTC(), Members: make([]RemoteMember, 0, len(bundle.Worktrees)),
	}
	for index, worktree := range bundle.Worktrees {
		memberSeed := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%s\x00%s\x00%s", request.ResumeID, index, worktree.Repository, worktree.Branch, worktree.Head)))
		request.Members = append(request.Members, RemoteMember{
			MemberID: fmt.Sprintf("m-%03d-%s", index+1, hex.EncodeToString(memberSeed[:4])), Repository: worktree.Repository,
			RepositoryRemote: worktree.RepositoryRemote, Branch: worktree.Branch, Commit: worktree.Head,
			SourceWorkLogReference: worktree.WorkLogReference,
		})
	}
	return request
}

func ReceiptSession(receipt Receipt) session.Record {
	return session.Record{PID: receipt.PID, WBSessionID: receipt.SuccessorWBSessionID,
		PredecessorWBSessionID: receipt.PredecessorWBSessionID, Machine: receipt.TargetMachine,
		Runtime: receipt.Runtime, Model: receipt.Model, NativeHarnessID: receipt.NativeHarnessID,
		TmuxName: receipt.TmuxName, HandoffID: receipt.ResumeID, StartedAt: receipt.StartedAt}
}
