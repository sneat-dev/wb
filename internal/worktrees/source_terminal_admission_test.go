package worktrees

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

// A sealed producer may be inspected while its projection or private evidence
// becomes unavailable. Each failure must return no claim before the outbox can
// authorize source admission; the native Git journey exercises the defaults.
func TestSealedSourceAdmissionRefusesChangedOrUnavailableEvidence(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{
		"projection unavailable", "projection active", "terminal disappeared",
		"private run unavailable", "immutable claim unreadable", "claim identity contradicted",
		"projection changed", "projection reread unavailable", "verified",
	} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			projection, claim, terminal, _ := testTerminalCleanupAuthority(t)
			beforeProjection, beforeTerminal := projection, terminal
			failure := errors.New("injected " + stage)
			projectionReads, claimReads, outboxReads := 0, 0, 0
			var opened *os.File
			ports := sealedSuccessSourcePorts{
				readProjection: func(string) (workLogProjection, error) {
					projectionReads++
					if projectionReads == 1 && stage == "projection unavailable" || projectionReads == 2 && stage == "projection reread unavailable" {
						return workLogProjection{}, failure
					}
					if projectionReads == 1 && stage == "projection active" {
						active := projection
						active.Lifecycle = "active"
						return active, nil
					}
					if projectionReads == 2 && stage == "projection changed" {
						changed := projection
						changed.ClaimID = "other-claim"
						return changed, nil
					}
					return projection, nil
				},
				readTerminal: func(string, string) (*workLogTerminalRecord, error) {
					if stage == "terminal disappeared" {
						return nil, nil
					}
					return &terminal, nil
				},
				openRun: func(string, string, string, bool) (*os.File, string, error) {
					if stage == "private run unavailable" {
						return nil, "", failure
					}
					var err error
					opened, err = os.Open(t.TempDir())
					return opened, "", err
				},
				readClaim: func(*os.File, string) (workLogClaim, error) {
					claimReads++
					if stage == "immutable claim unreadable" {
						return workLogClaim{}, failure
					}
					return claim, nil
				},
				corroborate: func(string, string, string, workLogProjection, workLogClaim) error {
					if stage == "claim identity contradicted" {
						return failure
					}
					return nil
				},
				validateOutbox: func(string, workLogClaim, workLogTerminalRecord) error {
					outboxReads++
					return nil
				},
			}
			got, err := ports.sealedSuccessSourceClaimReadOnly("home", "worktree", "sealed")
			if stage == "verified" {
				if err != nil || !reflect.DeepEqual(got, claim) || outboxReads != 1 || projectionReads != 2 {
					t.Fatalf("verified source = %+v, %v; outbox reads=%d projection reads=%d", got, err, outboxReads, projectionReads)
				}
			} else {
				if err == nil || !reflect.DeepEqual(got, workLogClaim{}) {
					t.Fatalf("%s returned a producer claim: %+v, %v", stage, got, err)
				}
				if stage == "projection changed" || stage == "terminal disappeared" || stage == "projection active" {
					if !strings.Contains(err.Error(), "projection") && !strings.Contains(err.Error(), "terminal") {
						t.Fatalf("%s refusal lost evidence reason: %v", stage, err)
					}
				} else if stage == "projection reread unavailable" {
					if !strings.Contains(err.Error(), failure.Error()) {
						t.Fatalf("%s refusal lost the original read error: %v", stage, err)
					}
				} else if !errors.Is(err, failure) {
					t.Fatalf("%s refusal lost the original read/identity error: %v", stage, err)
				}
				if stage != "projection changed" && stage != "projection reread unavailable" && outboxReads != 0 {
					t.Fatalf("%s read outbox after earlier authority failed", stage)
				}
			}
			if opened != nil {
				if _, statErr := opened.Stat(); !errors.Is(statErr, os.ErrClosed) {
					t.Fatalf("%s left the private run open: %v", stage, statErr)
				}
			}
			if !reflect.DeepEqual(projection, beforeProjection) || !reflect.DeepEqual(terminal, beforeTerminal) {
				t.Fatal("read-only source inspection changed supplied evidence")
			}
			if stage == "terminal disappeared" && claimReads != 0 {
				t.Fatal("claim was read after terminal disappeared")
			}
		})
	}
}
