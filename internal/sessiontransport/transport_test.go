package sessiontransport

import "testing"

func TestTargetIsZero(t *testing.T) {
	t.Parallel()
	if !(Target{}).IsZero() {
		t.Error("Target{}.IsZero() = false, want true")
	}
	if (Target{Kind: KindHerdr, ID: "w1:p2"}).IsZero() {
		t.Error("a populated Target reported IsZero() = true")
	}
}

// transportInterfaceCompileCheck fails to compile, not to run, if
// NoneTransport ever stops satisfying Transport.
var _ Transport = NoneTransport{}
