package remotestate

import (
	"strings"
	"testing"
	"time"
)

func TestClaimEncodeDecodeRoundTrip(t *testing.T) {
	claim := Claim{SchemaVersion: ClaimSchemaVersion, Task: "task-7", Login: "alice", Machine: "laptop",
		ClaimedAt: time.Date(2026, 8, 24, 9, 15, 0, 0, time.UTC), Note: "rehearsal"}
	data, err := EncodeClaim(claim)
	if err != nil {
		t.Fatal(err)
	}
	back, err := DecodeClaim(data)
	if err != nil {
		t.Fatal(err)
	}
	if !back.ClaimedAt.Equal(claim.ClaimedAt) {
		t.Fatalf("ClaimedAt round trip: %v != %v", back.ClaimedAt, claim.ClaimedAt)
	}
	back.ClaimedAt, claim.ClaimedAt = time.Time{}, time.Time{}
	if back != claim {
		t.Fatalf("round trip: %+v != %+v", back, claim)
	}
	if back.Holder() != "alice/laptop" {
		t.Fatalf("Holder() = %q", back.Holder())
	}
}

func TestDecodeClaimRejectsNewerSchema(t *testing.T) {
	if _, err := DecodeClaim([]byte("schema_version: 99\ntask: t\n")); err == nil || !strings.Contains(err.Error(), "schema_version 99") {
		t.Fatalf("err = %v, want newer-schema error", err)
	}
}

func TestDecodeClaimRejectsGarbage(t *testing.T) {
	if _, err := DecodeClaim([]byte("{nope")); err == nil {
		t.Fatal("expected YAML error")
	}
}

func TestValidTaskName(t *testing.T) {
	for _, ok := range []string{"task-7", "T1", "a.b_c-d"} {
		if err := ValidTaskName(ok); err != nil {
			t.Errorf("%q: unexpected error %v", ok, err)
		}
	}
	for _, bad := range []string{"", ".", "..", "-x", "a/b", "a b", ".hidden"} {
		if err := ValidTaskName(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}
