package orchestrate

import "github.com/sneat-dev/wb/internal/gitcli/gitclitest"

// var _ Git = (*gitclitest.Fake)(nil) proves gitclitest.Fake satisfies this
// package's own Git port at compile time, the same way gitcli.go's
// `var _ Git = gitcli.Client{}` proves the production adapter does.
var _ Git = (*gitclitest.Fake)(nil)
