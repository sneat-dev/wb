package main

// TestPeersReadParityAcrossHubAndLocalMounts is S5's read-parity test: the
// REAL /v0/workbench/peers route (through composeWorkbenchAPI, exactly as
// buildHubMount wires it) and the REAL /api/v1/peers route (through
// dashboard.NewHandler, exactly as serveDashboard wires it) must answer
// byte-identical JSON for the same underlying peer trust data — list and
// get-by-name alike, including status derivation (active/blocked) and
// node_id truncation.

// TestLaptopWithoutHubPeersAPIAnswersEmptyListNotHTML is B2's server-level
// regression test: a laptop daemon with no hub: section still mounts
// /api/v1/peers, answering an empty JSON list and a JSON 404 for a specific
// id — never the dashboard's HTML index, which is what broke `wb peers
// list`/`get` before this route was mounted unconditionally.

// TestEmptyPeersSourceIsAlwaysEmpty covers emptyPeersSource's two methods
// directly.

// TestHubMountPeersSourceIsNilSafe covers mount.peersSource()'s nil-receiver
// branch directly: a *hubMount that is nil (no hub section at all) answers
// nil, letting the caller fall back to emptyPeersSource uniformly.

// TestPeersViewerAuthorizeAlwaysAdmitsTheLoopbackOperator covers the
// authorize hook every peers mount wires: it matches the always-true
// loopback-operator viewer the sibling read routes already use, so it never
// refuses a request today.
