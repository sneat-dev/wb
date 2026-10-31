package sessioncourier

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionreceive"
	"github.com/sneat-dev/wb/internal/sessionterminal"
)

func TestCourierResponseRequiresOneExactTargetTerminal(t *testing.T) {
	request, raw := courierTestRequest(t)
	terminal := sessionterminal.Identity{
		Kind: "herdr", Socket: filepath.Join(t.TempDir(), "herdr.sock"),
		WorkspaceID: "workspace-owned", PaneID: "pane-owned",
	}
	newHerdrResult := func() sessionreceive.Result {
		result := validCourierResult(request, raw)
		result.Successor.Terminal, result.Receipt.Terminal = terminal, terminal
		result.Successor.TmuxName, result.Receipt.TmuxName = "", ""
		result.Successor.NativeHarnessID, result.Receipt.NativeHarnessID = "native-owned", "native-owned"
		return result
	}
	for name, result := range map[string]sessionreceive.Result{
		"matching legacy": validCourierResult(request, raw),
		"matching Herdr":  newHerdrResult(),
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateReceiverResult(result, request, raw); err != nil {
				t.Fatalf("matching target identities refused: %v", err)
			}
		})
	}
	mutations := map[string]func(*sessionreceive.Result){
		"successor socket":    func(r *sessionreceive.Result) { r.Successor.Terminal.Socket += ".other" },
		"receipt socket":      func(r *sessionreceive.Result) { r.Receipt.Terminal.Socket += ".other" },
		"successor workspace": func(r *sessionreceive.Result) { r.Successor.Terminal.WorkspaceID += "-other" },
		"receipt workspace":   func(r *sessionreceive.Result) { r.Receipt.Terminal.WorkspaceID += "-other" },
		"successor pane":      func(r *sessionreceive.Result) { r.Successor.Terminal.PaneID += "-other" },
		"receipt pane":        func(r *sessionreceive.Result) { r.Receipt.Terminal.PaneID += "-other" },
		"legacy successor with Herdr receipt": func(r *sessionreceive.Result) {
			r.Successor.Terminal = sessionterminal.Identity{}
			r.Successor.TmuxName = "wb-session-" + request.SuccessorWBSessionID
		},
		"Herdr successor with legacy receipt": func(r *sessionreceive.Result) {
			r.Receipt.Terminal = sessionterminal.Identity{}
			r.Receipt.TmuxName = "wb-session-" + request.SuccessorWBSessionID
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			result := newHerdrResult()
			mutate(&result)
			// Each side remains independently valid for this request. The refusal
			// must compare the generated target identities, not their shape alone.
			if err := sessionmove.ValidateReceiptForRequest(*result.Receipt, request, sessionmove.DigestBytes(raw)); err != nil {
				t.Fatalf("receipt fixture is not independently valid: %v", err)
			}
			if err := sessionterminal.ValidateBoundReceipt(request.SuccessorWBSessionID, result.Successor.TmuxName, result.Successor.Terminal); err != nil {
				t.Fatalf("successor fixture is not independently valid: %v", err)
			}
			if err := validateReceiverResult(result, request, raw); err == nil || !strings.Contains(err.Error(), "terminal identity does not match completion receipt") {
				t.Fatalf("conflicting target identities were not refused by equality guard: %v", err)
			}
		})
	}
}

func TestCourierResponseUsesExplicitRequestedModelInsteadOfSourceModel(t *testing.T) {
	request, _ := courierTestRequest(t)
	request.RequestedModel = "new-selected-model"
	raw, err := sessionmove.EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	result := validCourierResult(request, raw)
	if err := validateReceiverResult(result, request, raw); err != nil {
		t.Fatalf("selected model refused: %v", err)
	}
	result.Successor.Model = request.SourceModel
	if err := validateReceiverResult(result, request, raw); err == nil {
		t.Fatal("source model substituted for explicit successor selection")
	}
	result.Successor.Model = request.RequestedModel
	result.Receipt.Model = request.SourceModel
	if err := validateReceiverResult(result, request, raw); err == nil {
		t.Fatal("source model substituted for explicit receipt selection")
	}
}

func TestCourierCannotAcknowledgeLegacyDowngradeForExplicitHerdrRequest(t *testing.T) {
	request, _ := courierTestRequest(t)
	request.Transport = "herdr"
	raw, err := sessionmove.EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	result := validCourierResult(request, raw)
	// Both legacy addresses agree, so equality alone cannot enforce the
	// transport explicitly admitted by the request.
	if err := validateReceiverResult(result, request, raw); err == nil || !strings.Contains(err.Error(), "explicit Herdr request") {
		t.Fatalf("matching legacy downgrade accepted: %v", err)
	}
	result.Successor.TmuxName, result.Receipt.TmuxName = "", ""
	terminal := sessionterminal.Identity{Kind: "herdr", Socket: filepath.Join(t.TempDir(), "herdr.sock"), WorkspaceID: "workspace-owned", PaneID: "pane-owned"}
	result.Successor.Terminal, result.Receipt.Terminal = terminal, terminal
	if err := validateReceiverResult(result, request, raw); err != nil {
		t.Fatalf("matching Herdr result refused: %v", err)
	}
}
