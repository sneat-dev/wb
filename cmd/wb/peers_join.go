package main

import "github.com/sneat-dev/wb/internal/remotestate"

// sameOrigin remains a production-used server adapter until its own cutover.
func sameOrigin(a, b string) bool { return remotestate.SameOrigin(a, b) }
