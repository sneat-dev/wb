package main

import (
	"github.com/sneat-dev/wb/internal/cockpit"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

// newCockpitServer builds the Cockpit server one daemon run serves on
// address. It keeps the default session store, which is in memory: every
// owner session ends when the daemon restarts (cockpit#req:owner-session).
func newCockpitServer(address string, config wbconfig.CockpitConfig) *cockpit.Server {
	return cockpit.New(cockpit.Options{CanonicalHost: cockpit.CanonicalHost(address), Config: config})
}
