// Package shared defines the small contracts used by independent CLI families.
// It does not construct commands or perform domain operations.
package shared

// Flags is a snapshot of invocation-wide options. Families read only the fields
// they consume; the root registry remains responsible for persistent flag policy.
type Flags struct {
	ProjectsRoot   string
	Filter         string
	ExtraOrgs      []string
	NonInteractive bool
	Quiet          bool
}

// Runtime connects a family to invocation-wide policy without importing the
// executable package. Flags must be read during command execution, after parsing.
// ExitError preserves the executable's coded-error identity and classification.
type Runtime struct {
	Flags     func() Flags
	ExitError func(code int, message string) error
}

const (
	ExitFindings = 1
	ExitUsage    = 2
)
