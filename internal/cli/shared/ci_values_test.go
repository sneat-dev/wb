package shared

import (
	"github.com/spf13/cobra"
	"strings"
	"testing"
	"time"
)

func TestGitIdentityAndResumeQuoting(t *testing.T) {
	t.Parallel()
	if DefaultCIWaitSlice != 8*time.Minute {
		t.Fatalf("slice=%s", DefaultCIWaitSlice)
	}
	for _, length := range []int{40, 64} {
		if !ExactGitObjectID.MatchString(strings.Repeat("A", length)) {
			t.Fatalf("reject length%d", length)
		}
	}
	for _, value := range []string{"", strings.Repeat("a", 39), strings.Repeat("a", 41), strings.Repeat("g", 40)} {
		if ExactGitObjectID.MatchString(value) {
			t.Fatalf("accepted %q", value)
		}
	}
	for value, want := range map[string]string{"wb": "wb", "": "''", "a b": "'a b'", "a'b": `'a'"'"'b'`} {
		if got := ShellQuoteArg(value); got != want {
			t.Fatalf("quote=%q want %q", got, want)
		}
	}
}
func TestJSONFlagsShareOneOrderAwareSelector(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		args []string
		want bool
	}{{[]string{"--json", "--format=text"}, false}, {[]string{"--format=text", "--json"}, true}, {[]string{"--format=json", "--json=false"}, false}} {
		var jsonOut bool
		cmd := &cobra.Command{}
		AddJSONFormatFlags(cmd, &jsonOut)
		if OutputFormatChanged(cmd) {
			t.Fatal("untouched marked changed")
		}
		if err := cmd.ParseFlags(test.args); err != nil {
			t.Fatal(err)
		}
		if jsonOut != test.want || !OutputFormatChanged(cmd) {
			t.Fatalf("args=%v json=%t", test.args, jsonOut)
		}
	}
	var nilValue *jsonFormatValue
	if nilValue.String() != "text" || (&jsonFormatValue{}).String() != "text" || nilValue.Type() != "string" {
		t.Fatal("nil value contract")
	}
}
