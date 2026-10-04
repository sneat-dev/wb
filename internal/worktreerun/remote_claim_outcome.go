package worktreerun

// RemoteClaimOutcome carries a best-effort creation claim receipt.
// Disabled preserves the plain worktree result array; other outcomes include
// the receipt without changing the claim authority or creation success.
type RemoteClaimOutcome struct {
	Outcome string `json:"outcome"`
	Detail  string `json:"detail,omitempty"`
}
