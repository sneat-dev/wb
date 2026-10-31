// Package sessionterminal defines durable Herdr coordinates shared by session
// records and receipts. Legacy tmux names are readable historical data only.
package sessionterminal

import (
	"fmt"
	"github.com/sneat-dev/wb/internal/sessionauthority"
	"path/filepath"
	"regexp"
	"unicode"
	"unicode/utf8"
)

// Identity is scoped to one Herdr TUI server and one exact workspace/pane.
// The process generation and native harness ID remain on the enclosing record.
// Its zero value is omitted from old canonical JSON, preserving their digests.
type Identity struct {
	Kind        string `json:"kind"`
	Socket      string `json:"socket"`
	WorkspaceID string `json:"workspace_id"`
	PaneID      string `json:"pane_id"`
}

func (i Identity) IsZero() bool { return i == Identity{} }

func (i Identity) Validate() error {
	if i.Kind != "herdr" || !filepath.IsAbs(i.Socket) || filepath.Clean(i.Socket) != i.Socket {
		return fmt.Errorf("terminal requires Herdr TUI socket identity")
	}
	for _, value := range []string{i.Socket, i.WorkspaceID, i.PaneID} {
		if value == "" || len(value) > 4096 || !utf8.ValidString(value) {
			return fmt.Errorf("terminal coordinate is missing or too large")
		}
		for _, r := range value {
			if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
				return fmt.Errorf("terminal coordinate contains control text")
			}
		}
	}
	return nil
}

// ValidateReceipt preserves immutable legacy readers. Active send/launch paths
// separately require a nonzero Herdr identity and never invoke legacy names.
func ValidateReceipt(successorID, legacyName string, terminal Identity) error {
	if terminal.IsZero() {
		if !legacyReceiptID.MatchString(successorID) || successorID == "." || successorID == ".." {
			return fmt.Errorf("successor_wb_session_id is invalid")
		}
		if !legacyReceiptID.MatchString(legacyName) || legacyName == "." || legacyName == ".." {
			return fmt.Errorf("tmux_name is invalid")
		}

		return nil
	}
	if legacyName != "" {
		return fmt.Errorf("terminal receipt mixes Herdr and legacy identities")
	}
	return terminal.Validate()
}

var legacyReceiptID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidateBoundReceipt adds the deterministic historical address check at an
// authority boundary. Plain legacy decoders retain the original shape contract.
func ValidateBoundReceipt(successorID, legacyName string, terminal Identity) error {
	if err := ValidateReceipt(successorID, legacyName, terminal); err != nil {
		return err
	}
	if terminal.IsZero() && legacyName != "wb-session-"+successorID {
		return fmt.Errorf("successor tmux_name differs from the admitted successor identity")
	}
	return nil
}

// MatchesModel keeps legacy receipt matching exact. Only a new Herdr launch
// whose selected model was unspecified may enrich it with observed metadata.
func (terminal Identity) MatchesModel(expected, actual string) bool {
	if terminal.IsZero() {
		return expected == actual
	}
	return sessionauthority.ModelEnrichmentCompatible(expected, actual)
}
