package herdr

import (
	"errors"
	"testing"
)

func TestValidateTarget(t *testing.T) {
	cases := []struct {
		name    string
		target  string
		wantErr bool
	}{
		{name: "ordinary pane id", target: "w1:p2", wantErr: false},
		{name: "ordinary agent name", target: "reviewer", wantErr: false},
		{name: "empty", target: "", wantErr: true},
		{name: "leading hyphen", target: "-x", wantErr: true},
		{name: "leading double hyphen", target: "--", wantErr: true},
		{name: "hyphen not leading", target: "reviewer-session", wantErr: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateTarget("agent target", tc.target)
			if tc.wantErr && !errors.Is(err, ErrUnknownTarget) {
				t.Fatalf("ValidateTarget(%q) error = %v, want ErrUnknownTarget", tc.target, err)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateTarget(%q) error = %v, want nil", tc.target, err)
			}
		})
	}
}

func TestValidateTargetIncludesKindInMessage(t *testing.T) {
	err := ValidateTarget("pane id", "")
	if err == nil || !errors.Is(err, ErrUnknownTarget) {
		t.Fatalf("ValidateTarget() error = %v, want ErrUnknownTarget", err)
	}
	if got := err.Error(); got == "" {
		t.Fatal("ValidateTarget() error message is empty")
	}
}
