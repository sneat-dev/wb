package desk

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

const (
	testMachineID = "0123456789abcdef0123456789abcdef"
	testHash      = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

var testTime = time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)

func machine() MachineRef { return MachineRef{ID: testMachineID, Name: "macbook"} }

func startRequest() ConnectStartRequest {
	return ConnectStartRequest{SchemaVersion: SchemaVersion, Machine: machine(), WBVersion: "0.211.0",
		OS: "darwin", Arch: "arm64", Detail: LevelRepositories}
}

func startResponse() ConnectStartResponse {
	return ConnectStartResponse{DeviceCode: "dev-code_1", UserCode: "BCDF-GHJK",
		VerificationURI: "https://desk.sneat.dev/connect", VerificationURIComplete: "https://desk.sneat.dev/connect?code=BCDF-GHJK",
		ExpiresIn: 600, Interval: 5}
}

func issued() ConnectTokenResponse {
	return ConnectTokenResponse{Credential: "cred_1", Machine: machine(), PersonalSpaceID: "personal1",
		Shares: []Share{{SpaceID: "team1", Level: ShareRepositories, State: SharePending}}, Scopes: Scopes()}
}

func self() MachineSelf {
	return MachineSelf{Machine: machine(), PersonalSpaceID: "personal1", Detail: LevelFull,
		LastSnapshotHash: testHash, LastSnapshotAt: testTime, LastHeartbeatAt: testTime}
}

func snapshot() SnapshotRequest {
	return SnapshotRequest{SchemaVersion: SchemaVersion, Detail: LevelRepositories, TaskKeyID: "0123456789abcdef",
		ObservedAt: testTime, Hash: testHash, Envelope: json.RawMessage(`{"schema_version":1}`)}
}

func heartbeat() HeartbeatRequest {
	return HeartbeatRequest{SchemaVersion: SchemaVersion, ObservedAt: testTime, WBVersion: "0.211.0", LastSnapshotHash: testHash}
}

type validationCase struct {
	name  string
	value Validator
	ok    bool
}

func runValidation(t *testing.T, cases []validationCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.value.Validate()
			if tc.ok && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			if !tc.ok && !errors.Is(err, ErrInvalid) {
				t.Fatalf("Validate() = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestLevelsStatesAndCodesAcceptOnlyTheirClosedSets(t *testing.T) {
	t.Parallel()
	if !LevelRepositories.Valid() || !LevelFull.Valid() || Level("summary").Valid() {
		t.Fatal("machine detail levels are repositories and full only")
	}
	if !ShareSummary.Valid() || !ShareRepositories.Valid() || !ShareFull.Valid() || ShareLevel("all").Valid() {
		t.Fatal("share levels are summary, repositories and full")
	}
	if !SharePending.Valid() || !ShareActive.Valid() || ShareState("revoked").Valid() {
		t.Fatal("a grant returns only pending or active shares")
	}
	for _, code := range []GrantError{GrantPending, GrantSlowDown, GrantAccessDenied, GrantExpired} {
		if !code.Valid() {
			t.Fatalf("%s should be a grant error", code)
		}
	}
	if GrantError("other").Valid() {
		t.Fatal("unknown grant error accepted")
	}
	if len(ErrorCodes()) != 7 {
		t.Fatalf("ErrorCodes() = %v", ErrorCodes())
	}
	for _, code := range ErrorCodes() {
		if !code.Valid() {
			t.Fatalf("%s should be valid", code)
		}
	}
	if ErrorCode("Desk said no").Valid() {
		t.Fatal("free text accepted as an error code")
	}
	if got := Scopes(); len(got) != 3 || got[0] != ScopeSnapshotPublish || got[1] != ScopeHeartbeatWrite || got[2] != ScopeMachineReadSelf {
		t.Fatalf("Scopes() = %v", got)
	}
}

func TestConnectStartRequestRequiresAWellFormedMachine(t *testing.T) {
	t.Parallel()
	with := func(change func(*ConnectStartRequest)) ConnectStartRequest {
		request := startRequest()
		change(&request)
		return request
	}
	runValidation(t, []validationCase{
		{"valid", startRequest(), true},
		{"valid with space hint", with(func(r *ConnectStartRequest) { r.SpaceHint = "team1" }), true},
		{"dev build version", with(func(r *ConnectStartRequest) { r.WBVersion = "0.210.3+dirty" }), true},
		{"version with space", with(func(r *ConnectStartRequest) { r.WBVersion = "0.1 beta" }), false},
		{"other schema", with(func(r *ConnectStartRequest) { r.SchemaVersion = 2 }), false},
		{"bad machine id", with(func(r *ConnectStartRequest) { r.Machine.ID = "XYZ" }), false},
		{"bad machine name", with(func(r *ConnectStartRequest) { r.Machine.Name = "../etc" }), false},
		{"empty version", with(func(r *ConnectStartRequest) { r.WBVersion = "" }), false},
		{"bad os", with(func(r *ConnectStartRequest) { r.OS = "dar win" }), false},
		{"long arch", with(func(r *ConnectStartRequest) { r.Arch = strings.Repeat("a", MaxPlatformLength+1) }), false},
		{"summary detail", with(func(r *ConnectStartRequest) { r.Detail = "summary" }), false},
		{"bad space hint", with(func(r *ConnectStartRequest) { r.SpaceHint = "a b" }), false},
	})
}

func TestConnectStartResponseBoundsTheGrant(t *testing.T) {
	t.Parallel()
	with := func(change func(*ConnectStartResponse)) ConnectStartResponse {
		response := startResponse()
		change(&response)
		return response
	}
	runValidation(t, []validationCase{
		{"valid", startResponse(), true},
		{"empty device code", with(func(r *ConnectStartResponse) { r.DeviceCode = "" }), false},
		{"ambiguous user code", with(func(r *ConnectStartResponse) { r.UserCode = "AEIO-0123" }), false},
		{"plain http uri", with(func(r *ConnectStartResponse) { r.VerificationURI = "http://desk.sneat.dev/connect" }), false},
		{"long complete uri", with(func(r *ConnectStartResponse) {
			r.VerificationURIComplete = "https://" + strings.Repeat("a", MaxURILength)
		}), false},
		{"expiry over ten minutes", with(func(r *ConnectStartResponse) { r.ExpiresIn = MaxGrantSeconds + 1 }), false},
		{"zero interval", with(func(r *ConnectStartResponse) { r.Interval = 0 }), false},
		{"interval beyond expiry", with(func(r *ConnectStartResponse) { r.Interval = 601 }), false},
	})
}

func TestConnectTokenExchangeSeparatesGrantErrorsFromIssuedCredentials(t *testing.T) {
	t.Parallel()
	with := func(change func(*ConnectTokenResponse)) ConnectTokenResponse {
		response := issued()
		change(&response)
		return response
	}
	pendingWith := func(change func(*ConnectTokenResponse)) ConnectTokenResponse {
		response := ConnectTokenResponse{Error: GrantPending}
		change(&response)
		return response
	}
	tooMany := make([]Share, MaxShares+1)
	runValidation(t, []validationCase{
		{"token request", ConnectTokenRequest{DeviceCode: "dev-code_1"}, true},
		{"token request without code", ConnectTokenRequest{}, false},
		{"issued", issued(), true},
		{"issued with no shares", with(func(r *ConnectTokenResponse) { r.Shares = nil }), true},
		{"pending", ConnectTokenResponse{Error: GrantPending}, true},
		{"unknown error", ConnectTokenResponse{Error: "nope"}, false},
		{"error with credential", pendingWith(func(r *ConnectTokenResponse) { r.Credential = "x" }), false},
		{"error with machine", pendingWith(func(r *ConnectTokenResponse) { r.Machine = machine() }), false},
		{"error with space", pendingWith(func(r *ConnectTokenResponse) { r.PersonalSpaceID = "p" }), false},
		{"error with shares", pendingWith(func(r *ConnectTokenResponse) { r.Shares = []Share{{}} }), false},
		{"error with scopes", pendingWith(func(r *ConnectTokenResponse) { r.Scopes = Scopes() }), false},
		{"no credential", with(func(r *ConnectTokenResponse) { r.Credential = "" }), false},
		{"bad machine", with(func(r *ConnectTokenResponse) { r.Machine.ID = "" }), false},
		{"no personal space", with(func(r *ConnectTokenResponse) { r.PersonalSpaceID = "" }), false},
		{"too many shares", with(func(r *ConnectTokenResponse) { r.Shares = tooMany }), false},
		{"bad share space", with(func(r *ConnectTokenResponse) { r.Shares[0].SpaceID = "" }), false},
		{"share into personal space", with(func(r *ConnectTokenResponse) { r.Shares[0].SpaceID = "personal1" }), false},
		{"bad share level", with(func(r *ConnectTokenResponse) { r.Shares[0].Level = "everything" }), false},
		{"revoked share", with(func(r *ConnectTokenResponse) { r.Shares[0].State = "revoked" }), false},
		{"missing scope", with(func(r *ConnectTokenResponse) { r.Scopes = r.Scopes[:2] }), false},
		{"wider scope", with(func(r *ConnectTokenResponse) { r.Scopes[2] = "repository_events:poll" }), false},
	})
}

func TestMachineSelfPairsSnapshotHashAndTime(t *testing.T) {
	t.Parallel()
	with := func(change func(*MachineSelf)) MachineSelf {
		record := self()
		change(&record)
		return record
	}
	runValidation(t, []validationCase{
		{"valid", self(), true},
		{"never published", with(func(r *MachineSelf) { r.LastSnapshotHash, r.LastSnapshotAt = "", time.Time{} }), true},
		{"bad machine", with(func(r *MachineSelf) { r.Machine.Name = "" }), false},
		{"bad space", with(func(r *MachineSelf) { r.PersonalSpaceID = "a/b" }), false},
		{"bad detail", with(func(r *MachineSelf) { r.Detail = "" }), false},
		{"hash without time", with(func(r *MachineSelf) { r.LastSnapshotAt = time.Time{} }), false},
		{"malformed hash", with(func(r *MachineSelf) { r.LastSnapshotHash = "md5:00" }), false},
	})
}

func TestSnapshotRequestTiesTaskKeyToDetailLevel(t *testing.T) {
	t.Parallel()
	with := func(change func(*SnapshotRequest)) SnapshotRequest {
		request := snapshot()
		change(&request)
		return request
	}
	fullWithRepos := func(change func(*SnapshotRequest)) SnapshotRequest {
		request := SnapshotRequest{
			SchemaVersion:        SchemaVersion,
			Detail:               LevelFull,
			TaskKeyID:            "0123456789abcdef",
			ObservedAt:           testTime,
			Hash:                 testHash,
			Envelope:             json.RawMessage(`{"fleet":{"agents":[{"task":"secret-task"}]}}`),
			RepositoriesEnvelope: json.RawMessage(reducedEnvelope),
		}
		change(&request)
		return request
	}
	runValidation(t, []validationCase{
		{"repositories with key", snapshot(), true},
		{"full without key or repositories envelope", with(func(r *SnapshotRequest) { r.Detail, r.TaskKeyID = LevelFull, "" }), true},
		{"full with repositories envelope and key", fullWithRepos(func(*SnapshotRequest) {}), true},
		{"other schema", with(func(r *SnapshotRequest) { r.SchemaVersion = 0 }), false},
		{"bad detail", with(func(r *SnapshotRequest) { r.Detail = "summary" }), false},
		{"repositories with repositories envelope", with(func(r *SnapshotRequest) { r.RepositoriesEnvelope = json.RawMessage(`{}`) }), false},
		{"repositories without key", with(func(r *SnapshotRequest) { r.TaskKeyID = "" }), false},
		{"full with key but no repositories envelope", with(func(r *SnapshotRequest) { r.Detail = LevelFull }), false},
		{"full with repositories envelope without key", fullWithRepos(func(r *SnapshotRequest) { r.TaskKeyID = "" }), false},
		{"full with repositories envelope and malformed key", fullWithRepos(func(r *SnapshotRequest) { r.TaskKeyID = "secret-key" }), false},
		{"malformed key id", with(func(r *SnapshotRequest) { r.TaskKeyID = "XYZ" }), false},
		{"no observation time", with(func(r *SnapshotRequest) { r.ObservedAt = time.Time{} }), false},
		{"bad hash", with(func(r *SnapshotRequest) { r.Hash = "sha256:xyz" }), false},
		{"empty envelope", with(func(r *SnapshotRequest) { r.Envelope = nil }), false},
		{"envelope not an object", with(func(r *SnapshotRequest) { r.Envelope = json.RawMessage(`[1]`) }), false},
		{"envelope not json", with(func(r *SnapshotRequest) { r.Envelope = json.RawMessage(`{`) }), false},
		{"repositories envelope not an object", fullWithRepos(func(r *SnapshotRequest) { r.RepositoriesEnvelope = json.RawMessage(`[1]`) }), false},
		{"repositories envelope null", fullWithRepos(func(r *SnapshotRequest) { r.RepositoriesEnvelope = json.RawMessage(`null`) }), false},
		{"repositories envelope not json", fullWithRepos(func(r *SnapshotRequest) { r.RepositoriesEnvelope = json.RawMessage(`{`) }), false},
		{"repositories envelope carries withheld field", fullWithRepos(func(r *SnapshotRequest) {
			r.RepositoriesEnvelope = json.RawMessage(`{"fleet":{"agents":[{"task":"secret-task"}]}}`)
		}), false},
	})
}

func TestValidationErrorsNeverLeakFieldValues(t *testing.T) {
	t.Parallel()
	const sentinel = "secret-field-value"
	cases := map[string]Validator{
		"malformed task key": SnapshotRequest{
			SchemaVersion: SchemaVersion, Detail: LevelRepositories, TaskKeyID: sentinel,
			ObservedAt: testTime, Hash: testHash, Envelope: json.RawMessage(`{}`),
		},
		"repositories envelope at repositories detail": SnapshotRequest{
			SchemaVersion: SchemaVersion, Detail: LevelRepositories, TaskKeyID: "0123456789abcdef",
			ObservedAt: testTime, Hash: testHash, Envelope: json.RawMessage(`{}`),
			RepositoriesEnvelope: json.RawMessage(`{"note":"` + sentinel + `"}`),
		},
		"non-object repositories envelope": SnapshotRequest{
			SchemaVersion: SchemaVersion, Detail: LevelFull, TaskKeyID: "0123456789abcdef",
			ObservedAt: testTime, Hash: testHash, Envelope: json.RawMessage(`{}`),
			RepositoriesEnvelope: json.RawMessage(`"` + sentinel + `"`),
		},
		"withheld value in repositories envelope": SnapshotRequest{
			SchemaVersion: SchemaVersion, Detail: LevelFull, TaskKeyID: "0123456789abcdef",
			ObservedAt: testTime, Hash: testHash, Envelope: json.RawMessage(`{}`),
			RepositoriesEnvelope: json.RawMessage(`{"fleet":{"worktrees":[{"task":"` + sentinel + `"}]}}`),
		},
		"invalid heartbeat last_error": HeartbeatRequest{
			SchemaVersion: SchemaVersion, ObservedAt: testTime, WBVersion: "0.211.0",
			LastError: ErrorCode(sentinel),
		},
		"out of range heartbeat interval": HeartbeatRequest{
			SchemaVersion: SchemaVersion, ObservedAt: testTime, WBVersion: "0.211.0",
			IntervalSeconds: 999999,
		},
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := value.Validate()
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("Validate() = %v, want ErrInvalid", err)
			}
			if strings.Contains(err.Error(), sentinel) || strings.Contains(err.Error(), "999999") {
				t.Fatalf("Validate() leaked value in error: %v", err)
			}
		})
	}
}

func TestReceiptsHeartbeatsAndSchemaAnswers(t *testing.T) {
	t.Parallel()
	hb := func(change func(*HeartbeatRequest)) HeartbeatRequest {
		request := heartbeat()
		change(&request)
		return request
	}
	runValidation(t, []validationCase{
		{"receipt", SnapshotReceipt{Hash: testHash, AcceptedAt: testTime}, true},
		{"receipt without time", SnapshotReceipt{Hash: testHash}, false},
		{"heartbeat", heartbeat(), true},
		{"first heartbeat", hb(func(r *HeartbeatRequest) { r.LastSnapshotHash = "" }), true},
		{"heartbeat minimum interval", hb(func(r *HeartbeatRequest) { r.IntervalSeconds = 1 }), true},
		{"heartbeat maximum interval", hb(func(r *HeartbeatRequest) { r.IntervalSeconds = MaxHeartbeatIntervalSeconds }), true},
		{"heartbeat negative interval", hb(func(r *HeartbeatRequest) { r.IntervalSeconds = -1 }), false},
		{"heartbeat over-max interval", hb(func(r *HeartbeatRequest) { r.IntervalSeconds = MaxHeartbeatIntervalSeconds + 1 }), false},
		{"heartbeat with valid last error", hb(func(r *HeartbeatRequest) { r.LastError = ErrorTimeout }), true},
		{"heartbeat with invalid last error", hb(func(r *HeartbeatRequest) { r.LastError = "free text" }), false},
		{"heartbeat other schema", hb(func(r *HeartbeatRequest) { r.SchemaVersion = 9 }), false},
		{"heartbeat without time", hb(func(r *HeartbeatRequest) { r.ObservedAt = time.Time{} }), false},
		{"heartbeat without version", hb(func(r *HeartbeatRequest) { r.WBVersion = "" }), false},
		{"heartbeat bad hash", hb(func(r *HeartbeatRequest) { r.LastSnapshotHash = "x" }), false},
		{"newer minimum", SchemaUnsupported{MinimumSchemaVersion: SchemaVersion + 1}, true},
		{"minimum not newer", SchemaUnsupported{MinimumSchemaVersion: SchemaVersion}, false},
	})
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestDecodeIsStrictAndBounded(t *testing.T) {
	t.Parallel()
	var receipt SnapshotReceipt
	if err := Decode(strings.NewReader(`{"hash":"`+testHash+`","accepted_at":"2026-11-27T12:00:00Z"}`), MaxResponseBytes, &receipt); err != nil {
		t.Fatalf("Decode() = %v", err)
	}
	if !receipt.AcceptedAt.Equal(testTime) {
		t.Fatalf("decoded %+v", receipt)
	}
	cases := map[string]string{
		"unknown field": `{"hash":"` + testHash + `","accepted_at":"2026-11-27T12:00:00Z","note":"x"}`,
		"trailing data": `{"hash":"` + testHash + `","accepted_at":"2026-11-27T12:00:00Z"} {}`,
		"empty object":  `{}`,
		"invalid value": `{"hash":"x","accepted_at":"2026-11-27T12:00:00Z"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var target SnapshotReceipt
			if err := Decode(strings.NewReader(body), 64+int64(len(testHash))+40, &target); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Decode() = %v, want ErrInvalid", err)
			}
		})
	}
	if err := Decode(strings.NewReader(`{}`), 1, &receipt); !errors.Is(err, ErrInvalid) {
		t.Fatalf("over-limit body: %v", err)
	}
	if err := Decode(failingReader{}, 10, &receipt); err == nil || errors.Is(err, ErrInvalid) {
		t.Fatalf("read failure should surface as-is, got %v", err)
	}
}

func TestEncodingRoundTripsKeepEveryField(t *testing.T) {
	t.Parallel()
	for name, value := range map[string]Validator{
		"start": startRequest(), "grant": startResponse(), "issued": issued(),
		"self": self(), "snapshot": snapshot(), "heartbeat": heartbeat(),
	} {
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(body, &decoded); err != nil || len(decoded) == 0 {
			t.Fatalf("%s: %s", name, body)
		}
	}
	body, _ := json.Marshal(ConnectTokenResponse{Error: GrantSlowDown})
	if string(body) != `{"error":"slow_down"}` {
		t.Fatalf("a grant error encodes alone, got %s", body)
	}
	fullWithRepos := SnapshotRequest{
		SchemaVersion:        SchemaVersion,
		Detail:               LevelFull,
		TaskKeyID:            "0123456789abcdef",
		ObservedAt:           testTime,
		Hash:                 testHash,
		Envelope:             json.RawMessage(`{"schema_version":1}`),
		RepositoriesEnvelope: json.RawMessage(`{"schema_version":1}`),
	}
	encodedSnapshot, err := json.Marshal(fullWithRepos)
	if err != nil {
		t.Fatal(err)
	}
	var decodedSnapshot SnapshotRequest
	if err := Decode(strings.NewReader(string(encodedSnapshot)), MaxResponseBytes, &decodedSnapshot); err != nil {
		t.Fatalf("Decode(full snapshot with repositories_envelope) = %v", err)
	}
	if string(decodedSnapshot.RepositoriesEnvelope) != `{"schema_version":1}` {
		t.Fatalf("decoded repositories_envelope = %s", decodedSnapshot.RepositoriesEnvelope)
	}
	richHeartbeat := heartbeat()
	richHeartbeat.IntervalSeconds = 300
	richHeartbeat.LastError = ErrorTimeout
	encodedBeat, err := json.Marshal(richHeartbeat)
	if err != nil {
		t.Fatal(err)
	}
	var decodedBeat HeartbeatRequest
	if err := Decode(strings.NewReader(string(encodedBeat)), MaxResponseBytes, &decodedBeat); err != nil {
		t.Fatalf("Decode(heartbeat with interval and last_error) = %v", err)
	}
	if decodedBeat.IntervalSeconds != 300 || decodedBeat.LastError != ErrorTimeout {
		t.Fatalf("decoded heartbeat = %+v", decodedBeat)
	}
	plainBeat, _ := json.Marshal(heartbeat())
	if strings.Contains(string(plainBeat), "interval_seconds") || strings.Contains(string(plainBeat), "last_error") {
		t.Fatalf("empty optional heartbeat fields must be omitted: %s", plainBeat)
	}
	plainSnapshot, _ := json.Marshal(snapshot())
	if strings.Contains(string(plainSnapshot), "repositories_envelope") {
		t.Fatalf("empty repositories_envelope must be omitted: %s", plainSnapshot)
	}
}
