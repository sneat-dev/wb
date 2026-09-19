package herdr

import "testing"

func TestReadSourceCLIArg(t *testing.T) {
	cases := []struct {
		source ReadSource
		want   string
	}{
		{ReadSourceVisible, "visible"},
		{ReadSourceRecent, "recent"},
		{ReadSourceRecentUnwrapped, "recent-unwrapped"},
		{ReadSourceDetection, "detection"},
	}
	for _, tc := range cases {
		if got := tc.source.cliArg(); got != tc.want {
			t.Fatalf("%q.cliArg() = %q, want %q", tc.source, got, tc.want)
		}
	}
}

func TestRawScreenTextValid(t *testing.T) {
	if (rawScreenText{}).valid() {
		t.Fatal("zero-value rawScreenText reported valid")
	}
	if !(rawScreenText{PaneID: "w1:p1"}).valid() {
		t.Fatal("rawScreenText with a pane id reported invalid")
	}
}

func TestRawScreenTextToScreenText(t *testing.T) {
	raw := rawScreenText{
		PaneID:      "w1:p2",
		WorkspaceID: "w1",
		TabID:       "w1:t2",
		Source:      ReadSourceRecentUnwrapped,
		Format:      ReadFormatText,
		Text:        "hello",
		Revision:    3,
		Truncated:   true,
	}
	want := ScreenText{
		PaneID:      "w1:p2",
		WorkspaceID: "w1",
		TabID:       "w1:t2",
		Source:      ReadSourceRecentUnwrapped,
		Format:      ReadFormatText,
		Text:        "hello",
		Revision:    3,
		Truncated:   true,
	}
	if got := raw.toScreenText(); got != want {
		t.Fatalf("toScreenText() = %#v, want %#v", got, want)
	}
}
