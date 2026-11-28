package procalive

import "testing"

func TestParseProcStatStateHandlesCommSpacesAndParens(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		contents  string
		wantState byte
		wantOK    bool
		wantDead  bool
	}{
		{name: "sleeping", contents: "42 (codex) S 1 42 42 0 -1\n", wantState: 'S', wantOK: true},
		{name: "running", contents: "42 (codex) R 1 42 42 0 -1\n", wantState: 'R', wantOK: true},
		{name: "zombie single thread", contents: "42 (codex) Z 1 42 42 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0\n", wantState: 'Z', wantOK: true, wantDead: true},
		{name: "zombie zero threads", contents: "42 (codex) Z 1 42 42 0 -1 0 0 0 0 0 0 0 0 0 20 0 0 0\n", wantState: 'Z', wantOK: true, wantDead: true},
		{name: "zombie main thread with live workers", contents: "42 (codex) Z 1 42 42 0 -1 0 0 0 0 0 0 0 0 0 20 0 4 0\n", wantState: 'Z', wantOK: true},
		{name: "zombie truncated record", contents: "42 (codex) Z 1 42 42 0 -1\n", wantState: 'Z', wantOK: true},
		{name: "zombie non-numeric threads", contents: "42 (codex) Z 1 42 42 0 -1 0 0 0 0 0 0 0 0 0 20 0 x 0\n", wantState: 'Z', wantOK: true},
		{name: "zombie negative threads", contents: "42 (codex) Z 1 42 42 0 -1 0 0 0 0 0 0 0 0 0 20 0 -1 0\n", wantState: 'Z', wantOK: true},
		{name: "dead", contents: "42 (codex) X 1 42 42 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0\n", wantState: 'X', wantOK: true, wantDead: true},
		{name: "comm with spaces", contents: "42 (codex app server) Z 1 42 42 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0\n", wantState: 'Z', wantOK: true, wantDead: true},
		{name: "comm with nested parens zombie", contents: "42 (my (odd) worker) Z 1 42 42 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0\n", wantState: 'Z', wantOK: true, wantDead: true},
		{name: "comm with unmatched closing paren live", contents: "42 (my ) Z worker) S 1 42\n", wantState: 'S', wantOK: true},
		{name: "empty", contents: ""},
		{name: "missing closing paren", contents: "42 (codex Z 1 42"},
		{name: "nothing after closing paren", contents: "42 (codex)"},
		{name: "whitespace only after closing paren", contents: "42 (codex)   \n"},
		{name: "multi-char state field", contents: "42 (codex) ZZ 1 42\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			state, ok := parseProcStatState(tc.contents)
			if state != tc.wantState || ok != tc.wantOK {
				t.Fatalf("parseProcStatState(%q) = (%q, %t), want (%q, %t)", tc.contents, state, ok, tc.wantState, tc.wantOK)
			}
			if dead := isDeadProcStat([]byte(tc.contents)); dead != tc.wantDead {
				t.Fatalf("isDeadProcStat(%q) = %t, want %t", tc.contents, dead, tc.wantDead)
			}
		})
	}
}
