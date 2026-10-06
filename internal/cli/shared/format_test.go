package shared

import (
	"testing"
)

func TestRequireOutputFormatAcceptsOnlyExactAllowedValues(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"markdown", "yaml", "json"} {
		if err := RequireOutputFormat(value, "markdown", "yaml", "json"); err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range []string{"", "JSON", " json", "toml"} {
		err := RequireOutputFormat(value, "markdown", "yaml", "json")
		want := "unsupported format " + `"` + value + `"` + "; use markdown or yaml or json"
		if err == nil || err.Error() != want {
			t.Fatalf("format %q: error=%v want %q", value, err, want)
		}
	}
	if err := RequireOutputFormat("json"); err == nil || err.Error() != `unsupported format "json"; use ` {
		t.Fatalf("empty allowed list: %v", err)
	}
}
