package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/sessionparkreceive"
)

func TestSessionReceiveParkPassesExactEnvelopeAndPrintsOnlyReceipt(t *testing.T) {
	secret := "private continuation that must not be printed"
	raw := testReceiveParkEnvelope(t, secret)
	var received sessionparkreceive.Options
	deps := sessionReceiveParkDependencies{
		localMachine: func() (string, error) { return "target", nil },
		store: func(root string) (sessionpark.TargetStore, error) {
			if root != "/projects" {
				t.Fatalf("projects root = %q", root)
			}
			return sessionpark.NewTargetStore(t.TempDir()), nil
		},
		receive: func(_ context.Context, options sessionparkreceive.Options) (sessionparkreceive.Result, error) {
			received = options
			envelope, err := sessionpark.DecodeEnvelope(options.RawEnvelope)
			if err != nil {
				return sessionparkreceive.Result{}, err
			}
			digest := sessionmove.DigestBytes(options.RawEnvelope)
			request := envelope.Request
			return sessionparkreceive.Result{
				ResumeID: request.ResumeID, Digest: digest, Phase: sessionparkreceive.PhaseCompleted,
				Receipt: &sessionpark.Receipt{SuccessorWBSessionID: request.SuccessorWBSessionID, Members: make([]sessionpark.ReceiptMember, len(request.Members))},
			}, nil
		},
	}
	command := newSessionReceiveParkCmdWithDeps(deps)
	command.SetIn(bytes.NewReader(raw))
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	projectsRoot = "/projects"
	if err := command.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if received.LocalMachine != "target" || received.ProjectsRoot != "/projects" || !bytes.Equal(received.RawEnvelope, raw) {
		t.Fatalf("receiver options do not preserve authenticated exact input: %#v", received)
	}
	if strings.Contains(stdout.String(), secret) || strings.Contains(stdout.String(), sessionpark.ContinuationFileName) {
		t.Fatalf("stdout disclosed private continuation: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "durable target receipt") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestSessionReceiveParkRejectsOversizeBeforeDependencies(t *testing.T) {
	called := false
	command := newSessionReceiveParkCmdWithDeps(sessionReceiveParkDependencies{
		localMachine: func() (string, error) { called = true; return "", nil },
	})
	command.SetIn(bytes.NewReader(bytes.Repeat([]byte("x"), sessionpark.MaxEnvelopeBytes+1)))
	if err := command.ExecuteContext(context.Background()); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %v", err)
	}
	if called {
		t.Fatal("oversize input reached receiver dependencies")
	}
}

func TestSessionReceiveParkJSONDoesNotDiscloseContinuation(t *testing.T) {
	secret := "do not disclose this"
	raw := testReceiveParkEnvelope(t, secret)
	request, _ := sessionpark.DecodeEnvelope(raw)
	result := sessionparkreceive.Result{ResumeID: request.Request.ResumeID, Phase: sessionparkreceive.PhaseCompleted, Receipt: &sessionpark.Receipt{SuccessorWBSessionID: request.Request.SuccessorWBSessionID}}
	command := newSessionReceiveParkCmdWithDeps(sessionReceiveParkDependencies{
		localMachine: func() (string, error) { return "target", nil },
		store:        func(string) (sessionpark.TargetStore, error) { return sessionpark.NewTargetStore(t.TempDir()), nil },
		receive: func(context.Context, sessionparkreceive.Options) (sessionparkreceive.Result, error) {
			return result, nil
		},
	})
	command.SetArgs([]string{"--format", "json"})
	command.SetIn(bytes.NewReader(raw))
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	if err := command.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String(), secret) || strings.Contains(stdout.String(), sessionpark.ContinuationFileName) {
		t.Fatalf("JSON disclosed private continuation: %q", stdout.String())
	}
	var decoded sessionparkreceive.Result
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil || decoded.ResumeID != result.ResumeID {
		t.Fatalf("JSON result = %#v, err = %v", decoded, err)
	}
}

func testReceiveParkEnvelope(t *testing.T, continuation string) []byte {
	t.Helper()
	request := sessionpark.RemoteRequest{
		SchemaVersion: sessionpark.RequestSchemaVersion, ResumeID: "resume-0123456789abcdef0123456789abcdef",
		ParkedSessionID: "park-0123456789abcdef0123456789abcdef", SuccessorWBSessionID: "wb-target-0123456789abcdef",
		PredecessorWBSessionID: "wb-source-0123456789abcdef", SourceMachine: "source", TargetMachine: "target",
		SourceRuntime: "codex", Continuation: continuation, CreatedAt: time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC),
		Members: []sessionpark.RemoteMember{{MemberID: "member-0001", Repository: "acme/one", RepositoryRemote: "git@github.com:acme/one.git",
			Branch: "feature", Commit: strings.Repeat("a", 40), SourceWorkLogReference: "worklog:effort/run/" + strings.Repeat("b", 64)}},
	}
	raw, err := sessionpark.EncodeEnvelope(sessionpark.Envelope{SchemaVersion: sessionpark.EnvelopeSchemaVersion, Kind: sessionpark.EnvelopeKind, Request: request})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
