package sessiontransport

// Kind names one of the transport implementations WB ships. It is the value
// REQ:selected-transport-recorded persists on a session record (Task 5) and
// the value [ResolveOverride] validates an explicit override against.
type Kind string

// The transports WB ships. REQ:two-transport-implementations fixes the set
// at exactly herdr and tmux; [KindNone] is the first-class fallback
// REQ:none-transport-is-first-class requires when neither is observable.
const (
	KindHerdr Kind = "herdr"
	KindTmux  Kind = "tmux"
	KindNone  Kind = "none"
)

// Kinds lists every transport WB ships. [ResolveOverride] rejects any value
// outside this set; it carries no preference order — that belongs to
// automatic selection (REQ:automatic-transport-selection, Task 5's).
var Kinds = []Kind{KindHerdr, KindTmux, KindNone}

// Valid reports whether k names a transport WB actually ships. An override
// naming anything else is invalid input, never a fourth, unimplemented
// transport (REQ:explicit-transport-override).
func (k Kind) Valid() bool {
	switch k {
	case KindHerdr, KindTmux, KindNone:
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
