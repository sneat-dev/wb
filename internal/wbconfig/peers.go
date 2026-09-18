package wbconfig

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// errPeersUpstreamURL is returned by validatePeersUpstreamURL. The check is
// duplicated from internal/remotestate.ValidateHubURL — the same rule
// (https, or http only for loopback) applied to the same shape of value —
// rather than imported: internal/remotestate imports internal/worktrees,
// which (through internal/hooks) imports internal/wbconfig, so importing
// remotestate from here would be a cycle.
var errPeersUpstreamURL = errors.New("peers.upstream.url must be an https URL, or an http URL to localhost/127.0.0.1/::1, with no path, query, fragment or userinfo")

func validatePeersUpstreamURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return errPeersUpstreamURL
	}
	switch parsed.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopbackPeersHost(parsed.Hostname()) {
			return nil
		}
	}
	return errPeersUpstreamURL
}

func isLoopbackPeersHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// SetPeersUpstream writes wb.yaml's peers.upstream section
// (peer-connectivity#req:invite-and-join). It touches only the top-level
// "peers:" block, as raw text, and leaves every other byte of the file —
// including the entire `remote:` block, at whatever indentation, comment
// style, or key order the operator wrote it in — completely untouched.
// Remote state, claims and peer events are deliberately independent.
//
// This is a text splice rather than a whole-document YAML re-encode on
// purpose: a round-trip through a YAML encoder always reformats to that
// encoder's own canonical indentation and quoting, which silently rewrites
// an operator's non-canonical (but perfectly valid) formatting elsewhere in
// the file — exactly what "byte-identical" must never do. The document is
// still parsed once, read-only, to validate its shape (see
// validatePeersSectionShape) before any byte of it is touched.
func SetPeersUpstream(path, hubURL, tokenFile string) error {
	if err := validatePeersUpstreamURL(hubURL); err != nil {
		return fmt.Errorf("peers.upstream.url: %w", err)
	}
	if !filepath.IsAbs(tokenFile) {
		return fmt.Errorf("peers.upstream.token_file must be an absolute path, got %q", tokenFile)
	}

	raw, err := os.ReadFile(path)
	var original string
	switch {
	case err == nil:
		original = string(raw)
	case errors.Is(err, os.ErrNotExist):
		original = ""
	default:
		return fmt.Errorf("read config %s: %w", path, err)
	}
	if err := validatePeersSectionShape(original, path); err != nil {
		return err
	}

	urlLine, err := yamlScalarLine("    url", hubURL)
	if err != nil {
		return fmt.Errorf("encode peers.upstream.url: %w", err)
	}
	tokenLine, err := yamlScalarLine("    token_file", tokenFile)
	if err != nil {
		return fmt.Errorf("encode peers.upstream.token_file: %w", err)
	}
	newBlock := []string{"peers:", "  upstream:", urlLine, tokenLine}
	lines := spliceTopLevelBlock(splitLines(original), "peers", newBlock)
	updated := strings.Join(lines, "\n") + "\n"

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".wb-config-*.yaml")
	if err != nil {
		return fmt.Errorf("stage config: %w", err)
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("protect staged config: %w", err)
	}
	if _, err := temporary.WriteString(updated); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write config: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close config: %w", err)
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}

// validatePeersSectionShape parses text (a no-op on an empty/absent file)
// purely to check two things a splice cannot: the document is valid YAML at
// all, and an existing top-level "peers:" key is a mapping, not some other
// scalar or sequence a splice would otherwise silently graft an "upstream:"
// child onto.
func validatePeersSectionShape(text, path string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(text), &document); err != nil {
		return fmt.Errorf("parse config %s: %w", path, err)
	}
	if len(document.Content) == 0 {
		return nil
	}
	if document.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("parse config %s: top level must be a mapping", path)
	}
	if peers := mappingValue(document.Content[0], "peers"); peers != nil && peers.Kind != yaml.MappingNode {
		return fmt.Errorf("parse config %s: peers must be a mapping", path)
	}
	return nil
}

// splitLines splits text on "\n" without producing a synthetic trailing
// empty element for a "\n"-terminated string, so a caller comparing or
// rejoining line counts never has to special-case whether the original file
// ended with a trailing newline.
func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// spliceTopLevelBlock replaces the top-level "key:" block in lines — or
// appends newBlockLines at the end when key is absent — touching no other
// line. A block runs from a line matching exactly "key:" (or the single-line
// form "key: value", which no writer in this package ever produces but a
// hand-edited config could hold) through every following line that is blank
// or indented, up to but not including the next line that starts at column
// 0 with real content — a following top-level key or comment — or EOF.
func spliceTopLevelBlock(lines []string, key string, newBlockLines []string) []string {
	prefix := key + ":"
	start := -1
	for i, line := range lines {
		if line == prefix || strings.HasPrefix(line, prefix+" ") {
			start = i
			break
		}
	}
	if start == -1 {
		result := make([]string, 0, len(lines)+len(newBlockLines))
		result = append(result, lines...)
		result = append(result, newBlockLines...)
		return result
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		line := lines[i]
		if line == "" || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		end = i
		break
	}
	result := make([]string, 0, len(lines)-(end-start)+len(newBlockLines))
	result = append(result, lines[:start]...)
	result = append(result, newBlockLines...)
	result = append(result, lines[end:]...)
	return result
}

// yamlScalarLine renders "key: <value>" using the YAML library's own scalar
// encoding for value, so a value that needs quoting (leading special
// character, embedded colon-space, and so on) is quoted exactly as a full
// yaml.Marshal of the document would have quoted it, without this package
// hand-rolling YAML's quoting rules.
func yamlScalarLine(key, value string) (string, error) {
	encoded, err := yaml.Marshal(value)
	if err != nil {
		return "", err
	}
	return key + ": " + strings.TrimRight(string(encoded), "\n"), nil
}

// PeersUpstreamConfig is the peers.upstream section `wb peers list` reads on
// the laptop (upstream) side.
type PeersUpstreamConfig struct {
	URL       string `yaml:"url"`
	TokenFile string `yaml:"token_file"`
}

type peersConfigFile struct {
	Peers *struct {
		Upstream *PeersUpstreamConfig `yaml:"upstream"`
	} `yaml:"peers"`
}

// LoadPeersUpstream reads peers.upstream from path. found is false when the
// file, the peers section, or the upstream section is absent — the laptop
// simply has no configured hub yet, which is not an error. The section's
// contents are validated the same way SetPeersUpstream validates them before
// writing, so a hand-edited or corrupted config is refused clearly rather
// than handed to a caller as if it were a well-formed upstream.
func LoadPeersUpstream(path string) (PeersUpstreamConfig, bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return PeersUpstreamConfig{}, false, nil
	}
	if err != nil {
		return PeersUpstreamConfig{}, false, fmt.Errorf("read config %s: %w", path, err)
	}
	var file peersConfigFile
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return PeersUpstreamConfig{}, false, fmt.Errorf("parse config %s: %w", path, err)
	}
	if file.Peers == nil || file.Peers.Upstream == nil {
		return PeersUpstreamConfig{}, false, nil
	}
	upstream := *file.Peers.Upstream
	if err := validatePeersUpstreamURL(upstream.URL); err != nil {
		return PeersUpstreamConfig{}, false, fmt.Errorf("config %s: peers.upstream.url: %w", path, err)
	}
	if !filepath.IsAbs(upstream.TokenFile) {
		return PeersUpstreamConfig{}, false, fmt.Errorf("config %s: peers.upstream.token_file must be an absolute path, got %q", path, upstream.TokenFile)
	}
	return upstream, true, nil
}
