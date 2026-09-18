package sessionpark

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/sessionmove"
)

// spCovRemoteRequest builds one valid remote request from the shared remote
// fixture so every protocol assertion starts from admitted-shape input.
func spCovRemoteRequest(t *testing.T) RemoteRequest {
	t.Helper()
	return BuildRemoteRequest(remoteTestBundle(t), "target", "", time.Unix(100, 0).UTC())
}

// spCovEnvelopeFixture returns a valid envelope, its canonical bytes, and the
// digest the receiver would compute over those exact bytes.
func spCovEnvelopeFixture(t *testing.T) (Envelope, []byte, sessionmove.Digest) {
	t.Helper()
	envelope := Envelope{SchemaVersion: EnvelopeSchemaVersion, Kind: EnvelopeKind, Request: spCovRemoteRequest(t)}
	raw, err := EncodeEnvelope(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return envelope, raw, sessionmove.DigestBytes(raw)
}

func TestSpCovEnvelopeBoundsValidationAndStrictDecode(t *testing.T) {
	t.Parallel()
	envelope, raw, digest := spCovEnvelopeFixture(t)

	decoded, err := DecodeEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Request.Continuation != envelope.Request.Continuation ||
		decoded.Request.ResumeID != envelope.Request.ResumeID ||
		decoded.Request.Members[0].MemberID != envelope.Request.Members[0].MemberID ||
		!decoded.Request.CreatedAt.Equal(envelope.Request.CreatedAt) {
		t.Fatalf("round-trip envelope = %#v", decoded)
	}
	if digest.Matches(raw) != true {
		t.Fatalf("digest does not match its own bytes")
	}

	t.Run("empty and oversized decode input", func(t *testing.T) {
		t.Parallel()
		if _, err := DecodeEnvelope(nil); err == nil {
			t.Fatal("empty envelope accepted")
		}
		if _, err := DecodeEnvelope(make([]byte, MaxEnvelopeBytes+1)); err == nil {
			t.Fatal("oversized envelope accepted")
		}
	})
	t.Run("malformed JSON", func(t *testing.T) {
		t.Parallel()
		if _, err := DecodeEnvelope([]byte("{not json")); err == nil {
			t.Fatal("malformed envelope accepted")
		}
	})
	t.Run("unknown field rejected", func(t *testing.T) {
		t.Parallel()
		var value map[string]any
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatal(err)
		}
		value["surprise"] = "x"
		unknown, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeEnvelope(unknown); err == nil {
			t.Fatal("unknown envelope field accepted")
		}
	})
	t.Run("trailing JSON rejected", func(t *testing.T) {
		t.Parallel()
		if _, err := DecodeEnvelope(append(bytes.Clone(raw), []byte("{}")...)); err == nil {
			t.Fatal("trailing JSON accepted")
		}
	})
	t.Run("decoded but invalid envelope rejected", func(t *testing.T) {
		t.Parallel()
		var value map[string]any
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatal(err)
		}
		value["kind"] = "session_move"
		invalid, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeEnvelope(invalid); err == nil {
			t.Fatal("decoded envelope with wrong kind accepted")
		}
	})
	t.Run("envelope schema", func(t *testing.T) {
		t.Parallel()
		candidate := envelope
		candidate.SchemaVersion = SchemaVersion + 1
		if _, err := EncodeEnvelope(candidate); err == nil {
			t.Fatal("unsupported envelope schema accepted")
		}
	})
	t.Run("envelope kind", func(t *testing.T) {
		t.Parallel()
		candidate := envelope
		candidate.Kind = "session_move"
		if _, err := EncodeEnvelope(candidate); err == nil {
			t.Fatal("wrong envelope kind accepted")
		}
	})
	t.Run("oversized envelope bytes", func(t *testing.T) {
		t.Parallel()
		candidate := envelope
		candidate.Request.Members = []RemoteMember{{
			MemberID:               strings.Repeat("m", MaxEnvelopeBytes),
			Repository:             "acme/app",
			RepositoryRemote:       "https://github.com/acme/app.git",
			Branch:                 "feature/app",
			Commit:                 strings.Repeat("a", 40),
			SourceWorkLogReference: "worklog:effort/run/" + strings.Repeat("b", 64),
		}}
		if _, err := EncodeEnvelope(candidate); err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("oversized envelope error = %v", err)
		}
	})
}

func TestSpCovReceiptEncodeDecodeAndStrictBounds(t *testing.T) {
	t.Parallel()
	envelope, raw, digest := spCovEnvelopeFixture(t)
	receipt := validRemoteReceipt(t, RemoteAdmission{Envelope: envelope, Raw: raw, Digest: digest})
	encoded, err := EncodeReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeReceipt(encoded)
	if err != nil {
		t.Fatal(err)
	}
	reencoded, err := EncodeReceipt(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, reencoded) {
		t.Fatalf("receipt encoding is not stable:\n%s\n%s", encoded, reencoded)
	}

	t.Run("shape rejected on encode", func(t *testing.T) {
		t.Parallel()
		candidate := receipt
		candidate.SchemaVersion = 0
		if _, err := EncodeReceipt(candidate); err == nil {
			t.Fatal("invalid receipt shape accepted")
		}
	})
	t.Run("empty and oversized decode input", func(t *testing.T) {
		t.Parallel()
		if _, err := DecodeReceipt(nil); err == nil {
			t.Fatal("empty receipt accepted")
		}
		if _, err := DecodeReceipt(make([]byte, MaxEnvelopeBytes+1)); err == nil {
			t.Fatal("oversized receipt accepted")
		}
	})
	t.Run("malformed JSON", func(t *testing.T) {
		t.Parallel()
		if _, err := DecodeReceipt([]byte("{]")); err == nil {
			t.Fatal("malformed receipt accepted")
		}
	})
	t.Run("unknown field rejected", func(t *testing.T) {
		t.Parallel()
		var value map[string]any
		if err := json.Unmarshal(encoded, &value); err != nil {
			t.Fatal(err)
		}
		value["extra_member"] = 1
		unknown, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeReceipt(unknown); err == nil {
			t.Fatal("unknown receipt field accepted")
		}
	})
	t.Run("invalid shape rejected on decode", func(t *testing.T) {
		t.Parallel()
		candidate := receipt
		candidate.AttemptIndex = 0
		invalid, err := json.Marshal(candidate)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeReceipt(invalid); err == nil {
			t.Fatal("invalid receipt shape decoded")
		}
	})
}

func TestSpCovValidateReceiptRejectsEveryIdentityConflict(t *testing.T) {
	t.Parallel()
	envelope, raw, digest := spCovEnvelopeFixture(t)
	request := envelope.Request
	receipt := validRemoteReceipt(t, RemoteAdmission{Envelope: envelope, Raw: raw, Digest: digest})
	if err := ValidateReceipt(receipt, request, digest); err != nil {
		t.Fatal(err)
	}

	t.Run("invalid request", func(t *testing.T) {
		t.Parallel()
		candidate := request
		candidate.SchemaVersion = 0
		if err := ValidateReceipt(receipt, candidate, digest); err == nil {
			t.Fatal("receipt validated against invalid request")
		}
	})
	t.Run("invalid receipt shape", func(t *testing.T) {
		t.Parallel()
		candidate := receipt
		candidate.PID = 0
		if err := ValidateReceipt(candidate, request, digest); err == nil {
			t.Fatal("invalid receipt shape accepted")
		}
	})
	t.Run("identity conflicts", func(t *testing.T) {
		t.Parallel()
		for name, mutate := range map[string]func(*Receipt){
			"resume id":      func(value *Receipt) { value.ResumeID = "resume-other" },
			"request digest": func(value *Receipt) { value.RequestDigest = sessionmove.Digest("sha256:" + strings.Repeat("0", 64)) },
			"parked id":      func(value *Receipt) { value.ParkedSessionID = "park-other" },
			"successor":      func(value *Receipt) { value.SuccessorWBSessionID = "wbs-other" },
			"predecessor":    func(value *Receipt) { value.PredecessorWBSessionID = "wbs-other" },
			"target machine": func(value *Receipt) { value.TargetMachine = "other" },
			"tmux name":      func(value *Receipt) { value.TmuxName = "wb-session-other" },
			"member count": func(value *Receipt) {
				value.Members = append(append([]ReceiptMember(nil), value.Members...), value.Members[0])
			},
			"member key":    func(value *Receipt) { value.Members[0].MemberID = "m-999-00000000" },
			"member repo":   func(value *Receipt) { value.Members[0].Repository = "acme/other" },
			"member commit": func(value *Receipt) { value.Members[0].Commit = strings.Repeat("d", 40) },
			"member pin":    func(value *Receipt) { value.Members[0].Pin += "-other" },
			"member work log": func(value *Receipt) {
				value.Members[0].TargetWorkLogReference = "worklog:effort/run/" + strings.Repeat("e", 64)
			},
			"member work nope": func(value *Receipt) { value.Members[0].TargetWorkLogReference = "bogus" },
		} {
			t.Run(name, func(t *testing.T) {
				candidate := receipt
				candidate.Members = append([]ReceiptMember(nil), receipt.Members...)
				mutate(&candidate)
				if err := ValidateReceipt(candidate, request, digest); err == nil {
					t.Fatalf("conflicting receipt accepted: %#v", candidate)
				}
			})
		}
	})
	t.Run("harness identity conflicts", func(t *testing.T) {
		t.Parallel()
		for name, mutate := range map[string]func(*Receipt){
			"runtime": func(value *Receipt) { value.Runtime = "claude-code" },
			"model":   func(value *Receipt) { value.Model = "gpt-5" },
		} {
			t.Run(name, func(t *testing.T) {
				candidate := receipt
				candidate.Members = append([]ReceiptMember(nil), receipt.Members...)
				mutate(&candidate)
				if err := ValidateReceipt(candidate, request, digest); err == nil {
					t.Fatalf("harness-conflicting receipt accepted: %#v", candidate)
				}
			})
		}
	})
}

func TestSpCovTargetWorkLogReferenceBindsSourceClaim(t *testing.T) {
	t.Parallel()
	request := spCovRemoteRequest(t)
	_, _, digest := spCovEnvelopeFixture(t)
	member := request.Members[0]

	reference, err := TargetWorkLogReference(request, digest, member)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := sessionmove.ParseWorkLogReference(reference)
	if err != nil {
		t.Fatal(err)
	}
	source, err := sessionmove.ParseWorkLogReference(member.SourceWorkLogReference)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.EffortID != source.EffortID || parsed.RunID != source.RunID || parsed.ClaimID == source.ClaimID {
		t.Fatalf("derived reference = %#v from source %#v", parsed, source)
	}
	repeated, err := TargetWorkLogReference(request, digest, member)
	if err != nil {
		t.Fatal(err)
	}
	if repeated != reference {
		t.Fatalf("derivation is not deterministic: %q vs %q", repeated, reference)
	}

	t.Run("source reference rejected", func(t *testing.T) {
		t.Parallel()
		broken := member
		broken.SourceWorkLogReference = "bogus"
		if _, err := TargetWorkLogReference(request, digest, broken); err == nil {
			t.Fatal("invalid source Work Log reference accepted")
		}
	})
	t.Run("invalid digest rejected", func(t *testing.T) {
		t.Parallel()
		if _, err := TargetWorkLogReference(request, sessionmove.Digest(""), member); err == nil {
			t.Fatal("invalid digest accepted")
		}
	})
}

func TestSpCovTargetWorkLogClaimIDRejectsInvalidIdentity(t *testing.T) {
	t.Parallel()
	_, raw, digest := spCovEnvelopeFixture(t)
	request := spCovRemoteRequest(t)
	source, err := sessionmove.ParseWorkLogReference(request.Members[0].SourceWorkLogReference)
	if err != nil {
		t.Fatal(err)
	}
	if !digest.Matches(raw) {
		t.Fatal("fixture digest does not match")
	}

	valid, err := TargetWorkLogClaimID(digest, request.SuccessorWBSessionID, request.Members[0].MemberID, request.Members[0].Repository, source.ClaimID)
	if err != nil {
		t.Fatal(err)
	}
	if !validSHA256Hex(valid) {
		t.Fatalf("claim ID %q is not 64 lowercase hex characters", valid)
	}
	again, err := TargetWorkLogClaimID(digest, request.SuccessorWBSessionID, request.Members[0].MemberID, request.Members[0].Repository, source.ClaimID)
	if err != nil || again != valid {
		t.Fatalf("claim derivation is not deterministic: %q vs %q err=%v", again, valid, err)
	}
	other, err := TargetWorkLogClaimID(digest, request.SuccessorWBSessionID, request.Members[0].MemberID, "acme/other", source.ClaimID)
	if err != nil || other == valid {
		t.Fatalf("repository did not change the claim ID: %q vs %q err=%v", other, valid, err)
	}

	for name, call := range map[string]func() (string, error){
		"invalid digest": func() (string, error) {
			return TargetWorkLogClaimID("", request.SuccessorWBSessionID, request.Members[0].MemberID, "acme/app", source.ClaimID)
		},
		"invalid successor": func() (string, error) {
			return TargetWorkLogClaimID(digest, "..", request.Members[0].MemberID, "acme/app", source.ClaimID)
		},
		"invalid member key": func() (string, error) {
			return TargetWorkLogClaimID(digest, request.SuccessorWBSessionID, "..", "acme/app", source.ClaimID)
		},
		"empty repository": func() (string, error) {
			return TargetWorkLogClaimID(digest, request.SuccessorWBSessionID, request.Members[0].MemberID, "  ", source.ClaimID)
		},
		"oversized repository": func() (string, error) {
			return TargetWorkLogClaimID(digest, request.SuccessorWBSessionID, request.Members[0].MemberID, strings.Repeat("r", MaxFieldBytes+1), source.ClaimID)
		},
		"multiline repository": func() (string, error) {
			return TargetWorkLogClaimID(digest, request.SuccessorWBSessionID, request.Members[0].MemberID, "acme/app\n", source.ClaimID)
		},
		"invalid source claim": func() (string, error) {
			return TargetWorkLogClaimID(digest, request.SuccessorWBSessionID, request.Members[0].MemberID, "acme/app", "not-hex")
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := call(); err == nil {
				t.Fatal("invalid claim identity accepted")
			}
		})
	}
}

func TestSpCovLaunchAuthorityPublishesExactRequestIdentity(t *testing.T) {
	t.Parallel()
	envelope, raw, digest := spCovEnvelopeFixture(t)
	request := envelope.Request
	continuationPath := filepath.Join(t.TempDir(), "successor-context.md")
	continuation := []byte(request.Continuation + "\nTarget worktrees:\n")

	launch, err := LaunchAuthority(request, digest, continuationPath, continuation)
	if err != nil {
		t.Fatal(err)
	}
	if launch.AggregateID != request.ResumeID || launch.AggregateDigest != string(digest) || launch.AggregateFile != EnvelopeFileName ||
		launch.SuccessorWBSessionID != request.SuccessorWBSessionID || launch.PredecessorWBSessionID != request.PredecessorWBSessionID ||
		launch.TargetMachine != request.TargetMachine || launch.SourceRuntime != request.SourceRuntime || launch.SourceModel != request.SourceModel ||
		launch.RequestedHarness != request.RequestedHarness || launch.ContinuationKind != "private" ||
		launch.ContinuationPath != continuationPath || launch.ContinuationDigest != string(sessionmove.DigestBytes(continuation)) ||
		launch.PinnedCommit != request.Members[0].Commit || launch.PinnedBranch != MemberPin(request.ResumeID, request.Members[0].MemberID) ||
		launch.RootMode != "pinned_clean" {
		t.Fatalf("launch authority = %#v", launch)
	}
	if !digest.Matches(raw) {
		t.Fatal("digest does not bind the admitted envelope")
	}

	t.Run("invalid request", func(t *testing.T) {
		t.Parallel()
		broken := request
		broken.SchemaVersion = 0
		if _, err := LaunchAuthority(broken, digest, continuationPath, continuation); err == nil {
			t.Fatal("launch authority accepted invalid request")
		}
	})
	t.Run("empty continuation", func(t *testing.T) {
		t.Parallel()
		if _, err := LaunchAuthority(request, digest, continuationPath, nil); err == nil {
			t.Fatal("empty continuation accepted")
		}
	})
	t.Run("oversized continuation", func(t *testing.T) {
		t.Parallel()
		if _, err := LaunchAuthority(request, digest, continuationPath, make([]byte, MaxSuccessorContextBytes+1)); err == nil {
			t.Fatal("oversized continuation accepted")
		}
	})
	t.Run("relative continuation path", func(t *testing.T) {
		t.Parallel()
		if _, err := LaunchAuthority(request, digest, "relative/context.md", continuation); err == nil {
			t.Fatal("relative private continuation path accepted")
		}
	})
}

func TestSpCovLocalLaunchAuthorityModesAndRejections(t *testing.T) {
	t.Parallel()
	bundle := testBundle(t)
	raw, err := EncodeBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	digest := sessionmove.DigestBytes(raw)
	continuationPath := filepath.Join(t.TempDir(), "local-context.md")
	continuation := []byte(bundle.Continuation + "\nRetained local worktrees:\n")

	launch, err := LocalLaunchAuthority(bundle, digest, continuationPath, continuation)
	if err != nil {
		t.Fatal(err)
	}
	if launch.AggregateID != bundle.ParkedSessionID || launch.AggregateFile != BundleFileName ||
		launch.PredecessorWBSessionID != bundle.Source.WBSessionID || launch.TargetMachine != bundle.Source.Machine ||
		launch.SourceRuntime != bundle.Source.Runtime || launch.RootMode != "parked_local" ||
		launch.PinnedCommit != bundle.Worktrees[0].Head || launch.PinnedBranch != bundle.Worktrees[0].Branch ||
		launch.ContinuationDigest != string(sessionmove.DigestBytes(continuation)) {
		t.Fatalf("local launch authority = %#v", launch)
	}
	if launch.SuccessorWBSessionID == "" || !strings.HasPrefix(launch.SuccessorWBSessionID, "wbs-") {
		t.Fatalf("derived successor = %q", launch.SuccessorWBSessionID)
	}
	repeated, err := LocalLaunchAuthority(bundle, digest, continuationPath, continuation)
	if err != nil || repeated.SuccessorWBSessionID != launch.SuccessorWBSessionID {
		t.Fatalf("successor derivation is not deterministic: %#v err=%v", repeated, err)
	}
	otherBundle := bundle
	otherBundle.ParkedSessionID = "park-test-two"
	otherRaw, err := EncodeBundle(otherBundle)
	if err != nil {
		t.Fatal(err)
	}
	otherLaunch, err := LocalLaunchAuthority(otherBundle, sessionmove.DigestBytes(otherRaw), continuationPath, continuation)
	if err != nil {
		t.Fatal(err)
	}
	if otherLaunch.SuccessorWBSessionID == launch.SuccessorWBSessionID {
		t.Fatal("distinct parked sessions derived the same successor")
	}

	t.Run("zero worktrees uses neutral root", func(t *testing.T) {
		t.Parallel()
		neutral := bundle
		neutral.Worktrees = nil
		neutralRaw, err := EncodeBundle(neutral)
		if err != nil {
			t.Fatal(err)
		}
		neutralLaunch, err := LocalLaunchAuthority(neutral, sessionmove.DigestBytes(neutralRaw), continuationPath, []byte(neutral.Continuation))
		if err != nil {
			t.Fatal(err)
		}
		if neutralLaunch.RootMode != "parked_neutral" || neutralLaunch.PinnedCommit != "" || neutralLaunch.PinnedBranch != "" {
			t.Fatalf("neutral launch authority = %#v", neutralLaunch)
		}
	})
	t.Run("invalid bundle", func(t *testing.T) {
		t.Parallel()
		broken := bundle
		broken.SchemaVersion = 0
		if _, err := LocalLaunchAuthority(broken, digest, continuationPath, continuation); err == nil {
			t.Fatal("invalid bundle accepted")
		}
	})
	t.Run("invalid digest", func(t *testing.T) {
		t.Parallel()
		if _, err := LocalLaunchAuthority(bundle, "", continuationPath, continuation); err == nil {
			t.Fatal("invalid digest accepted")
		}
	})
	t.Run("empty continuation", func(t *testing.T) {
		t.Parallel()
		if _, err := LocalLaunchAuthority(bundle, digest, continuationPath, nil); err == nil {
			t.Fatal("empty continuation accepted")
		}
	})
	t.Run("unrelated continuation", func(t *testing.T) {
		t.Parallel()
		if _, err := LocalLaunchAuthority(bundle, digest, continuationPath, []byte("unrelated")); err == nil {
			t.Fatal("unrelated continuation accepted")
		}
	})
	t.Run("relative continuation path", func(t *testing.T) {
		t.Parallel()
		if _, err := LocalLaunchAuthority(bundle, digest, "relative.md", continuation); err == nil {
			t.Fatal("relative continuation path accepted")
		}
	})
}

func TestSpCovValidateRequestRejectsEveryIncompleteIdentity(t *testing.T) {
	t.Parallel()
	request := spCovRemoteRequest(t)
	for name, mutate := range map[string]func(*RemoteRequest){
		"schema":             func(value *RemoteRequest) { value.SchemaVersion = 0 },
		"resume id":          func(value *RemoteRequest) { value.ResumeID = ".." },
		"parked id":          func(value *RemoteRequest) { value.ParkedSessionID = ".." },
		"successor":          func(value *RemoteRequest) { value.SuccessorWBSessionID = ".." },
		"predecessor":        func(value *RemoteRequest) { value.PredecessorWBSessionID = ".." },
		"source machine":     func(value *RemoteRequest) { value.SourceMachine = ".." },
		"target machine":     func(value *RemoteRequest) { value.TargetMachine = ".." },
		"zero created at":    func(value *RemoteRequest) { value.CreatedAt = time.Time{} },
		"blank runtime":      func(value *RemoteRequest) { value.SourceRuntime = "  " },
		"newline runtime":    func(value *RemoteRequest) { value.SourceRuntime = "codex\n" },
		"carriage model":     func(value *RemoteRequest) { value.SourceModel = "gpt\r" },
		"newline harness":    func(value *RemoteRequest) { value.RequestedHarness = "codex\n" },
		"empty continuation": func(value *RemoteRequest) { value.Continuation = "" },
		"oversize continuation": func(value *RemoteRequest) {
			value.Continuation = strings.Repeat("c", MaxContinuationBytes+1)
		},
		"invalid UTF-8 continuation": func(value *RemoteRequest) { value.Continuation = "\xff\xfe" },
		"zero members":               func(value *RemoteRequest) { value.Members = nil },
		"too many members":           func(value *RemoteRequest) { value.Members = make([]RemoteMember, MaxMembers+1) },
		"duplicate member": func(value *RemoteRequest) {
			value.Members = append(append([]RemoteMember(nil), value.Members...), value.Members[0])
		},
		"invalid member": func(value *RemoteRequest) {
			value.Members = append([]RemoteMember(nil), value.Members...)
			value.Members[0].MemberID = ".."
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			candidate := request
			candidate.Members = append([]RemoteMember(nil), request.Members...)
			mutate(&candidate)
			if _, err := EncodeEnvelope(Envelope{SchemaVersion: EnvelopeSchemaVersion, Kind: EnvelopeKind, Request: candidate}); err == nil {
				t.Fatalf("invalid request accepted: %#v", candidate)
			}
		})
	}
}

func TestSpCovValidateRemoteMemberRejectsUnsafeFields(t *testing.T) {
	t.Parallel()
	member := spCovRemoteRequest(t).Members[0]
	for name, mutate := range map[string]func(*RemoteMember){
		"member key":             func(value *RemoteMember) { value.MemberID = ".." },
		"empty repository":       func(value *RemoteMember) { value.Repository = "  " },
		"oversized repository":   func(value *RemoteMember) { value.Repository = strings.Repeat("r", MaxFieldBytes+1) },
		"multiline repository":   func(value *RemoteMember) { value.Repository = "acme/app\n" },
		"empty branch":           func(value *RemoteMember) { value.Branch = "" },
		"empty source reference": func(value *RemoteMember) { value.SourceWorkLogReference = "" },
		"credentialed remote":    func(value *RemoteMember) { value.RepositoryRemote = "https://user:secret@github.com/acme/app.git" },
		"remote repository mismatch": func(value *RemoteMember) {
			value.RepositoryRemote = "https://github.com/acme/other.git"
		},
		"unsafe remote": func(value *RemoteMember) { value.RepositoryRemote = "-oProxyCommand=boom" },
		"short commit":  func(value *RemoteMember) { value.Commit = strings.Repeat("a", 12) },
		"uppercase commit": func(value *RemoteMember) {
			value.Commit = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
		},
		"unparsable source reference": func(value *RemoteMember) { value.SourceWorkLogReference = "worklog:a/b" },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			candidate := member
			mutate(&candidate)
			if err := validateRemoteMember(candidate); err == nil {
				t.Fatalf("unsafe member accepted: %#v", candidate)
			}
		})
	}
	if err := validateRemoteMember(member); err != nil {
		t.Fatalf("valid member rejected: %v", err)
	}
}

func TestSpCovValidateReceiptShapeRejectsIncompleteFields(t *testing.T) {
	t.Parallel()
	envelope, raw, digest := spCovEnvelopeFixture(t)
	receipt := validRemoteReceipt(t, RemoteAdmission{Envelope: envelope, Raw: raw, Digest: digest})
	if err := validateReceiptShape(receipt); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Receipt){
		"schema":             func(value *Receipt) { value.SchemaVersion = 0 },
		"resume id":          func(value *Receipt) { value.ResumeID = ".." },
		"parked id":          func(value *Receipt) { value.ParkedSessionID = ".." },
		"successor":          func(value *Receipt) { value.SuccessorWBSessionID = ".." },
		"predecessor":        func(value *Receipt) { value.PredecessorWBSessionID = ".." },
		"target machine":     func(value *Receipt) { value.TargetMachine = ".." },
		"tmux name":          func(value *Receipt) { value.TmuxName = ".." },
		"empty digest":       func(value *Receipt) { value.RequestDigest = "" },
		"blank runtime":      func(value *Receipt) { value.Runtime = " " },
		"attempt id":         func(value *Receipt) { value.AttemptID = "1-bad" },
		"zero attempt index": func(value *Receipt) { value.AttemptIndex = 0 },
		"zero pid":           func(value *Receipt) { value.PID = 0 },
		"zero started at":    func(value *Receipt) { value.StartedAt = time.Time{} },
		"zero members":       func(value *Receipt) { value.Members = nil },
		"too many members":   func(value *Receipt) { value.Members = make([]ReceiptMember, MaxMembers+1) },
		"member key":         func(value *Receipt) { value.Members[0].MemberID = ".." },
		"empty repository":   func(value *Receipt) { value.Members[0].Repository = "" },
		"relative target":    func(value *Receipt) { value.Members[0].TargetPath = "relative/path" },
		"unclean target":     func(value *Receipt) { value.Members[0].TargetPath = "/tmp/../escape" },
		"empty pin":          func(value *Receipt) { value.Members[0].Pin = "" },
		"short commit":       func(value *Receipt) { value.Members[0].Commit = "abc" },
		"bad work log":       func(value *Receipt) { value.Members[0].TargetWorkLogReference = "nope" },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			candidate := receipt
			candidate.Members = append([]ReceiptMember(nil), receipt.Members...)
			mutate(&candidate)
			if err := validateReceiptShape(candidate); err == nil {
				t.Fatalf("incomplete receipt accepted: %#v", candidate)
			}
		})
	}
}

func TestSpCovStrictDecodeRejectsTrailingValuesAndUnknownFields(t *testing.T) {
	t.Parallel()
	type spCovShape struct {
		Value int `json:"value"`
	}
	var decoded spCovShape
	if err := strictDecode([]byte(`{"value":7}`), &decoded); err != nil || decoded.Value != 7 {
		t.Fatalf("decoded=%#v err=%v", decoded, err)
	}
	if err := strictDecode([]byte(`{"value":7}{"value":8}`), &decoded); err == nil {
		t.Fatal("second JSON value accepted")
	}
	if err := strictDecode([]byte(`{"value":7} trailing`), &decoded); err == nil {
		t.Fatal("trailing garbage accepted")
	}
	if err := strictDecode([]byte(`{"unknown":1}`), &decoded); err == nil {
		t.Fatal("unknown field accepted")
	}
}
