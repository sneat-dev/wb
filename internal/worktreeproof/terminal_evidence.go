package worktreeproof

import "time"

type DirtyWorktreeEvidence struct {
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
	Files  int    `json:"files"`
}

type OrphanedEvidence struct {
	Version            int       `json:"version"`
	ObservedAt         time.Time `json:"observed_at"`
	Actor              string    `json:"actor"`
	Reason             string    `json:"reason"`
	WorktreeAbsent     bool      `json:"worktree_absent"`
	RegistrationAbsent bool      `json:"registration_absent"`
	LocalBranchAbsent  bool      `json:"local_branch_absent"`
	RemoteBranchAbsent bool      `json:"remote_branch_absent"`
	TerminalAbsent     bool      `json:"terminal_absent"`
}
