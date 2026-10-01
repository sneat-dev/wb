package worktreeretire

import (
	"context"
	"fmt"
	"time"

	"github.com/sneat-dev/wb/internal/worktreebranches"
)

// ApplyPorts contains the checkout and authority operations that must remain
// in the caller. Each port is bound to one held retirement task and checkout.
type ApplyPorts struct {
	ValidateHeld         func() error
	CommitSource         func(context.Context, string, func(string, string) error) (bool, error)
	ValidateIntentCommit func(context.Context, Transaction, string) error
	CurrentHead          func(context.Context) (string, error)
	Clean                func(context.Context) (bool, error)
	SealWorkLog          func(Transaction) error
	CheckPrivateArchive  func(context.Context, Transaction) error
	PublishArchive       func(context.Context, *Transaction) error
	BeforeDeletion       func(context.Context) error
	RemoveLocal          func(context.Context, *Transaction) error
}

// Operation is scoped to one held task. It owns phase persistence and proof
// order while the facade keeps authority checks and descriptor-bound mutation.
type Operation struct {
	Remote        TransactionPorts
	Apply         ApplyPorts
	ArchiveRemote string
	Now           func() time.Time
	AfterPhase    func(string) error
	WriteReport   func(Transaction) error
}

func (operation Operation) write(result Transaction) error {
	if operation.WriteReport != nil {
		return operation.WriteReport(result)
	}
	return WriteReport(result)
}

func (operation Operation) after(phase string) error {
	if operation.AfterPhase != nil {
		return operation.AfterPhase(phase)
	}
	return nil
}

// ApplyTransaction resumes from the recorded intent or creates one before any
// commit, then advances only after each exact remote receipt is verified.
func (operation Operation) ApplyTransaction(ctx context.Context, result Transaction, prior bool, current string, message string) (Transaction, error) {
	if prior {
		if result.Phase == "commit_intent" {
			if current == result.IntentParentSHA {
				committed, err := operation.Apply.CommitSource(ctx, result.IntentMessage, func(tree, message string) error {
					if tree != result.IntentTreeSHA || message != result.IntentMessage {
						return fmt.Errorf("staged source changed after retirement commit intent")
					}
					return nil
				})
				if err != nil {
					return result, err
				}
				if !committed {
					return result, fmt.Errorf("retirement commit intent lost its staged changes")
				}
				current, err = operation.Apply.CurrentHead(ctx)
				if err != nil {
					return result, err
				}
			}
			if err := operation.Apply.ValidateIntentCommit(ctx, result, current); err != nil {
				return result, err
			}
			if err := operation.after("source_committed"); err != nil {
				return result, err
			}
			result.SourceSHA = current
			result.RetiredRef = worktreebranches.RetiredBranchDestination(result.IntentAt, result.Branch, current)
			result.ArchiveRef = ArchiveRef(result)
			result.Phase = "committed"
			if err := operation.write(result); err != nil {
				return result, err
			}
		} else if current != result.SourceSHA {
			return Transaction{}, fmt.Errorf("checkout moved after recorded retirement commit")
		}
	} else {
		if err := operation.Apply.ValidateHeld(); err != nil {
			return Transaction{}, err
		}
		committed, err := operation.Apply.CommitSource(ctx, message, func(tree, message string) error {
			result.IntentParentSHA = current
			result.IntentTreeSHA = tree
			result.IntentMessage = message
			result.IntentAt = operation.Now().UTC()
			result.Phase = "commit_intent"
			return operation.write(result)
		})
		if err != nil {
			return Transaction{}, err
		}
		if committed {
			if err := operation.after("source_committed"); err != nil {
				return result, err
			}
		}
		result.SourceSHA, err = operation.Apply.CurrentHead(ctx)
		if err != nil {
			return Transaction{}, err
		}
		if clean, cleanErr := operation.Apply.Clean(ctx); cleanErr != nil || !clean {
			return Transaction{}, fmt.Errorf("source checkout is dirty after retirement commit: %w", cleanErr)
		}
		date := operation.Now()
		if committed {
			date = result.IntentAt
		}
		result.RetiredRef = worktreebranches.RetiredBranchDestination(date, result.Branch, result.SourceSHA)
		result.ArchiveRef = ArchiveRef(result)
		result.Phase = "committed"
		if err := operation.write(result); err != nil {
			return Transaction{}, err
		}
	}
	if err := PublishSource(ctx, &result, operation.Remote); err != nil {
		return result, err
	}
	if err := operation.write(result); err != nil {
		return result, err
	}
	if err := operation.after(result.Phase); err != nil {
		return result, err
	}
	if err := operation.Apply.SealWorkLog(result); err != nil {
		return result, fmt.Errorf("seal retirement Work Log: %w", err)
	}
	if err := operation.Apply.CheckPrivateArchive(ctx, result); err != nil {
		return result, err
	}
	if err := operation.Apply.PublishArchive(ctx, &result); err != nil {
		return result, err
	}
	if err := operation.write(result); err != nil {
		return result, err
	}
	if err := operation.after(result.Phase); err != nil {
		return result, err
	}
	if err := VerifyReceipts(ctx, result.Canonical, operation.ArchiveRemote, result, operation.Remote); err != nil {
		return result, err
	}
	if err := operation.Apply.CheckPrivateArchive(ctx, result); err != nil {
		return result, err
	}
	if err := operation.Apply.BeforeDeletion(ctx); err != nil {
		return result, err
	}
	if err := operation.PrepareDeletionIntent(ctx, &result); err != nil {
		return result, err
	}
	if err := DeleteOriginal(ctx, &result, operation.AfterPhase, operation.Remote); err != nil {
		return result, err
	}
	if err := operation.write(result); err != nil {
		return result, err
	}
	if err := operation.after(result.Phase); err != nil {
		return result, err
	}
	if err := operation.Apply.RemoveLocal(ctx, &result); err != nil {
		return result, err
	}
	if err := operation.write(result); err != nil {
		return result, err
	}
	return result, nil
}

func (operation Operation) PrepareDeletionIntent(ctx context.Context, result *Transaction) error {
	if result.OriginalRemoteSHA == "" || result.DeleteIntentSHA != "" {
		return nil
	}
	current, err := operation.Remote.RemoteSHA(ctx, result.Canonical, "origin", "refs/heads/"+result.Branch)
	if err != nil || current != result.OriginalRemoteSHA {
		return fmt.Errorf("original remote branch disappeared or moved before deletion intent: %w", err)
	}
	proof, err := operation.Remote.RemoteSHA(ctx, result.Canonical, "origin", DeletionProofRef(*result))
	if err != nil || proof != "" {
		return fmt.Errorf("retirement deletion proof ref is already present or cannot be inspected: %w", err)
	}
	result.DeleteIntentSHA = result.OriginalRemoteSHA
	if err := operation.write(*result); err != nil {
		return err
	}
	return operation.after("original_delete_intent")
}

// VerifyRemovedRemote applies the same receipt rules to a checkout that has
// already disappeared, then checks the original-ref deletion proof exactly.
func (operation Operation) VerifyRemovedRemote(ctx context.Context, result Transaction) error {
	if err := VerifyReceipts(ctx, result.Canonical, operation.ArchiveRemote, result, operation.Remote); err != nil {
		return err
	}
	source, err := operation.Remote.RemoteSHA(ctx, result.Canonical, "origin", "refs/heads/"+result.Branch)
	if err != nil || source != "" {
		return fmt.Errorf("original remote branch remains or changed: %w", err)
	}
	if result.OriginalRemoteSHA != "" {
		proof, err := operation.Remote.RemoteSHA(ctx, result.Canonical, "origin", DeletionProofRef(result))
		if err != nil || proof != result.SourceSHA {
			return fmt.Errorf("original remote branch deletion proof changed: %w", err)
		}
	}
	return nil
}

func (operation Operation) CompleteRemoved(result *Transaction) error {
	result.Phase = "complete"
	return operation.write(*result)
}

func ValidateRemovedReceipt(result Transaction, path, task, repository string) error {
	if repository != "" && result.Repository != repository {
		return fmt.Errorf("retirement receipt repository mismatch")
	}
	if result.Task != task || result.ReportPath != path || result.Phase != "original_deleted" && result.Phase != "complete" {
		return fmt.Errorf("retirement receipt does not authorize removed-checkout resume")
	}
	return nil
}
