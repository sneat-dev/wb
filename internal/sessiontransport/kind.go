package sessiontransport

// Kind names one of the transport implementations WB ships. It is the value
// REQ:selected-transport-recorded persists on a session record (Task 5) and
// the value [ResolveOverride] validates an explicit override against.
type Kind string

// Herdr is the sole active terminal transport; none retains record-only
// operation without a terminal. Legacy tmux receipts remain immutable data.
const (
	KindHerdr Kind = "herdr"
	KindNone  Kind = "none"
)

// Kinds returns every transport WB ships, in a fresh slice each call.
// [ResolveOverride] rejects any value outside this set; it carries no
// preference order — that belongs to automatic selection
// (REQ:automatic-transport-selection, Task 5's). It is a function, not an
// exported slice var, so no caller can corrupt the shipped set for the rest
// of the process by mutating what it got back.
func Kinds() []Kind {
	return []Kind{KindHerdr, KindNone}
}

// Valid reports whether k names a transport WB actually ships. An override
// naming anything else is invalid input, never a fourth, unimplemented
// transport (REQ:explicit-transport-override).
func (k Kind) Valid() bool {
	switch k {
	case KindHerdr, KindNone:
		return true
	default:
		return false
	}
}

// String implements fmt.Stringer so a Kind reads plainly in an error or log
// line rather than as a quoted Go string constant.
func (k Kind) String() string {
	return string(k)
}
