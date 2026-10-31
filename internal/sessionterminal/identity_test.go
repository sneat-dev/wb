package sessionterminal

import (
	"encoding/json"
	"testing"
)

func TestLegacyCanonicalJSONOmitsTerminalIdentity(t *testing.T) {
	t.Parallel()
	type receipt struct {
		ID       string   `json:"id"`
		Terminal Identity `json:"terminal,omitzero"`
	}
	raw, err := json.Marshal(receipt{ID: "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"id":"legacy"}` {
		t.Fatalf("legacy canonical bytes changed: %s", raw)
	}
	herdr := Identity{Kind: "herdr", Socket: "/tmp/herdr.sock", WorkspaceID: "w1", PaneID: "w1:p1"}
	if err := herdr.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(receipt{ID: "new", Terminal: herdr})
	if err != nil {
		t.Fatal(err)
	}
	var restored receipt
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Terminal != herdr {
		t.Fatal("Herdr value identity changed across JSON")
	}
	if err := ValidateReceipt("wbs-old", "wb-session-wbs-old", Identity{}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateReceipt("wbs-new", "", herdr); err != nil {
		t.Fatal(err)
	}
	if err := ValidateReceipt("wbs-new", "wb-session-wbs-new", herdr); err == nil {
		t.Fatal("mixed terminal identity accepted")
	}
}

func TestLegacyShapesRemainReadableWhileAuthorityBindsDeterministicAddress(t *testing.T) {
	t.Parallel()
	if err := ValidateReceipt("wbs-old", "other-safe-name", Identity{}); err != nil {
		t.Fatalf("legacy shape reader was tightened: %v", err)
	}
	if err := ValidateBoundReceipt("wbs-old", "other-safe-name", Identity{}); err == nil {
		t.Fatal("legacy address mismatch passed authority boundary")
	}
	if err := ValidateBoundReceipt("wbs-old", "wb-session-wbs-old", Identity{}); err != nil {
		t.Fatal(err)
	}
	endpoint := Identity{Kind: "herdr", Socket: "/tmp/owned.sock", WorkspaceID: "w1", PaneID: "w1:p7"}
	if err := ValidateBoundReceipt("wbs-new", "", endpoint); err != nil {
		t.Fatal(err)
	}
	if err := ValidateBoundReceipt("wbs-new", "legacy", endpoint); err == nil {
		t.Fatal("mixed active endpoint accepted")
	}
	if (Identity{}).MatchesModel("unknown", "known") || !endpoint.MatchesModel("unknown", "known") || endpoint.MatchesModel("requested", "different") {
		t.Fatal("model enrichment changed legacy or selected-known model contract")
	}
}
