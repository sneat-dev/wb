package agents

import (
	"os"
	"strings"
)

// defaultPath is used only when the invoking process has no PATH at all, so a
// worker is dispatched with a usable one instead of failing at exec time.
const defaultPath = "/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

// WorkerEnvironment builds the child harness environment.
//
// It is an allowlist, following WB's existing detached-worker convention: a
// delegated worker gets the variables it genuinely needs and nothing else. That
// is a stronger guarantee than any deny-list, because a secret this machine
// happens to export tomorrow is excluded by default rather than by pattern.
//
// Neither a whole-environment copy nor an ambient-agent-variable inheritance is
// acceptable here: the first hands a model-driven process every credential on
// the machine, and the second would make a dispatched worker look like the
// parent session it must not disturb.
func WorkerEnvironment(credentialEnv string) []string {
	allow := []string{
		"PATH", "HOME", "TMPDIR", "TMP", "TEMP",
		"LANG", "LC_ALL", "LC_CTYPE", "TZ",
		"XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME",
		// Transport configuration the harness's own process needs to reach its
		// provider. These carry no credentials: they name a proxy or a
		// certificate authority. Withholding them makes a dispatched worker
		// unreachable on a proxied or private-CA machine, which is a
		// functionality loss with no security gain — the harness's tool
		// subprocesses are already stopped from inheriting secrets by the
		// harness's own shell-environment policy.
		"SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS",
		"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy",
		"NO_PROXY", "no_proxy", "ALL_PROXY", "all_proxy",
	}
	environment := make([]string, 0, len(allow)+1)
	for _, name := range allow {
		if value, ok := os.LookupEnv(name); ok && value != "" {
			environment = append(environment, name+"="+value)
		}
	}
	if !hasValue(environment, "PATH") {
		// A worker with no usable PATH cannot resolve its own harness, so an
		// absent *or empty* one is filled from the platform default rather
		// than left to fail obscurely at exec time.
		environment = append(environment, "PATH="+defaultPath)
	}
	// The one credential the resolved provider actually needs is re-added
	// explicitly. Its *value* travels only in the child's environment: it never
	// reaches an argument list, a log, or a run record.
	if name := strings.TrimSpace(credentialEnv); name != "" {
		if value, ok := os.LookupEnv(name); ok && value != "" {
			environment = append(environment, name+"="+value)
		}
	}
	return environment
}

// hasValue reports whether environment carries a non-empty value for name.
func hasValue(environment []string, name string) bool {
	prefix := name + "="
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) && strings.TrimPrefix(entry, prefix) != "" {
			return true
		}
	}
	return false
}

// MissingCredential reports the credential variable a launch needs but the
// process environment does not carry, so the failure names the exact variable
// instead of surfacing as an opaque provider rejection later.
func MissingCredential(credentialEnv string) (string, bool) {
	name := strings.TrimSpace(credentialEnv)
	if name == "" {
		return "", true
	}
	if value, ok := os.LookupEnv(name); !ok || strings.TrimSpace(value) == "" {
		return name, true
	}
	return name, false
}

// pathValue is the PATH a Git helper child should inherit.
func pathValue() string {
	if value := strings.TrimSpace(os.Getenv("PATH")); value != "" {
		return value
	}
	return defaultPath
}

// homeDir is the HOME a Git helper child should inherit.
func homeDir() string {
	if value := strings.TrimSpace(os.Getenv("HOME")); value != "" {
		return value
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}
