package diskprune

import "github.com/sneat-dev/wb/internal/goenv"

// GoEnv is the subset of Go's configuration prune depends on, resolved
// read-only by internal/goenv.
type GoEnv = goenv.Env

// ResolveGoEnv resolves Go's settings without running go.
func ResolveGoEnv() (GoEnv, error) { return goenv.Resolve() }
