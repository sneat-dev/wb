package wbconfig

import (
	"bytes"
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
// "peers:" key and leaves every other byte of the file — including the
// entire `remote:` block, at whatever indentation, comment style, line
// ending or key order the operator wrote it in — untouched. Remote state,
// claims and peer events are deliberately independent.
//
// The document is parsed with yaml.v3's Node API to find the exact line
// range the existing "peers:" key (if any) occupies — matching it by its
// decoded value, not by scanning literal text, so a quoted key, a CRLF
// line ending, "peers :" with a space before the colon, or a single-line
// flow-style value are all found correctly instead of being missed and
// silently duplicated. Only that line range (or, if absent, an insertion
// point before a trailing "..." document-end marker, or at true EOF) is
// replaced; the rest of the original text is copied through byte for byte.
// Sibling keys already under "peers:" are preserved, not dropped, because
// the existing node holding them is mutated in place rather than replaced.
//
// Before ever renaming the staged file into place, verifySpliceResult
// re-parses the result and refuses — leaving the original file completely
// untouched — if it does not decode back to exactly the url and token_file
// just written, or if anything outside the "peers:" key changed. This is
// the backstop for whatever edge case the line-range logic above does not
// handle correctly: a bug there must surface as a clean refusal, never as
// a silently corrupted config file.
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

	updated, err := splicePeersUpstream(original, hubURL, tokenFile)
	if err != nil {
		return fmt.Errorf("update config %s: %w", path, err)
	}
	if err := verifySpliceResult(original, updated, hubURL, tokenFile); err != nil {
		return fmt.Errorf("refusing to write config %s: %w", path, err)
	}

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

// verifySpliceResult is SetPeersUpstream's pre-write safety check: it
// re-parses updated exactly as an operator's next `wb peers list` would
// (via parsePeersUpstream) and independently confirms every top-level key
// besides "peers" is byte-identical between original and updated, by
// stripping the "peers" key from each with the same line-range logic and
// comparing what remains.
func verifySpliceResult(original, updated, hubURL, tokenFile string) error {
	upstream, found, err := parsePeersUpstream([]byte(updated))
	if err != nil {
		return fmt.Errorf("result does not parse: %w", err)
	}
	if !found || upstream.URL != hubURL || upstream.TokenFile != tokenFile {
		return fmt.Errorf("result peers.upstream = %+v (found=%t), want url=%q token_file=%q", upstream, found, hubURL, tokenFile)
	}
	beforeOutsidePeers, err := stripTopLevelKey(original, "peers")
	if err != nil {
		return fmt.Errorf("re-check original: %w", err)
	}
	afterOutsidePeers, err := stripTopLevelKey(updated, "peers")
	if err != nil {
		return fmt.Errorf("re-check result: %w", err)
	}
	// Trimmed only of trailing newline characters: inserting new content
	// after a file that did not itself end with one requires adding exactly
	// one newline first (new YAML content cannot start on the same physical
	// line as existing content) — a fix-up, not a content change — so it
	// must not itself trip this refusal. Any other difference still does.
	if strings.TrimRight(beforeOutsidePeers, "\r\n") != strings.TrimRight(afterOutsidePeers, "\r\n") {
		return errors.New("a key other than peers changed")
	}
	return nil
}

// splicePeersUpstream is SetPeersUpstream's core: it returns text with only
// the top-level "peers:" key's mapping value updated (url and token_file
// set or added under "upstream:", every other existing field of "peers:"
// preserved) or, if "peers:" does not exist yet, added.
func splicePeersUpstream(original, hubURL, tokenFile string) (string, error) {
	newline := "\n"
	if strings.Contains(original, "\r\n") {
		newline = "\r\n"
	}

	_, root, err := parseYAMLDocument(original)
	if err != nil {
		return "", err
	}

	upstreamValue := &yaml.Node{Kind: yaml.MappingNode}
	setMappingScalar(upstreamValue, "url", hubURL)
	setMappingScalar(upstreamValue, "token_file", tokenFile)

	peersKeyIndex := findTopLevelKeyIndex(root, "peers")
	var peersKey, peersValue *yaml.Node
	if peersKeyIndex >= 0 {
		peersKey = root.Content[peersKeyIndex]
		peersValue = root.Content[peersKeyIndex+1]
		if isEmptyYAML(peersValue) {
			// "peers:" with nothing after it (a null scalar): treat as an
			// empty mapping the operator meant to fill in, not an error.
			peersValue.Kind, peersValue.Tag, peersValue.Value, peersValue.Content = yaml.MappingNode, "", "", nil
		} else if peersValue.Kind != yaml.MappingNode {
			return "", errors.New("peers must be a mapping")
		}
		// Force block style even if the original was flow ("peers: {...}"):
		// the rendering below always emits block style, and leaving a stale
		// FlowStyle tag on the node would fight the encoder.
		peersValue.Style = 0
		if upstreamIndex := findMappingKeyIndex(peersValue, "upstream"); upstreamIndex >= 0 {
			peersValue.Content[upstreamIndex+1] = upstreamValue
		} else {
			peersValue.Content = append(peersValue.Content, scalar("upstream"), upstreamValue)
		}
	} else {
		peersKey = scalar("peers")
		peersValue = &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{scalar("upstream"), upstreamValue}}
	}

	rendered, err := renderKeyValuePair(peersKey, peersValue)
	if err != nil {
		return "", err
	}
	renderedLines := splitLinesKeepEnds(convertNewlines(rendered, newline))

	lines := splitLinesKeepEnds(original)
	if peersKeyIndex >= 0 {
		startLine := peersKey.Line
		endLineExclusive := topLevelKeyEndBoundary(root, peersKeyIndex, lines)
		return joinLines(spliceLines(lines, startLine, endLineExclusive, renderedLines, newline)), nil
	}
	insertionLine := documentInsertionPoint(lines)
	return joinLines(spliceLines(lines, insertionLine, insertionLine, renderedLines, newline)), nil
}

// stripTopLevelKey removes key's entire line range from text (or returns
// text unchanged if key is absent), using the exact same boundary logic
// splicePeersUpstream uses to insert it — so a value it can insert, it can
// also cleanly remove, and the two stay consistent by construction.
func stripTopLevelKey(text, key string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return text, nil
	}
	_, root, err := parseYAMLDocument(text)
	if err != nil {
		return "", err
	}
	index := findTopLevelKeyIndex(root, key)
	if index < 0 {
		return text, nil
	}
	lines := splitLinesKeepEnds(text)
	startLine := root.Content[index].Line
	endLineExclusive := topLevelKeyEndBoundary(root, index, lines)
	return joinLines(spliceLines(lines, startLine, endLineExclusive, nil, lineEndingOf(text))), nil
}

// parseYAMLDocument parses text into a document node and returns its root
// mapping, creating an empty mapping for an empty/absent file. It refuses
// anything whose top level is not a mapping.
func parseYAMLDocument(text string) (*yaml.Node, *yaml.Node, error) {
	var document yaml.Node
	if strings.TrimSpace(text) != "" {
		if err := yaml.Unmarshal([]byte(text), &document); err != nil {
			return nil, nil, fmt.Errorf("parse config: %w", err)
		}
	}
	if len(document.Content) == 0 {
		document = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	}
	root := document.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, nil, errors.New("top level must be a mapping")
	}
	return &document, root, nil
}

// findTopLevelKeyIndex returns the index of key's key-node within mapping's
// Content (so its value is at index+1), matching by the node's decoded
// value — not its raw source text — so a quoted key ("peers") or one with
// unusual surrounding whitespace (peers :) is still found. -1 if absent.
func findTopLevelKeyIndex(mapping *yaml.Node, key string) int {
	return findMappingKeyIndex(mapping, key)
}

func findMappingKeyIndex(mapping *yaml.Node, key string) int {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return i
		}
	}
	return -1
}

// isEmptyYAML reports whether node is YAML's null (an omitted or explicit
// "~"/"null" value) — the shape "peers:" with nothing after it parses to.
func isEmptyYAML(node *yaml.Node) bool {
	return node.Kind == yaml.ScalarNode && (node.Tag == "!!null" || node.Value == "")
}

// topLevelKeyEndBoundary returns the 1-indexed line number (exclusive) where
// the key at mapping.Content[index] ends: the line the next top-level key
// starts on, or — for the last key — a trailing "..." document-end marker's
// line if present (a document-end marker "puts" any text appended after it
// into a second YAML document, which a single-document parse never sees, so
// new content must be inserted before it, not after), else one past EOF.
func topLevelKeyEndBoundary(mapping *yaml.Node, index int, lines []string) int {
	if index+2 < len(mapping.Content) {
		return mapping.Content[index+2].Line
	}
	if marker := documentEndMarkerLine(lines); marker > 0 {
		return marker
	}
	return len(lines) + 1
}

// documentInsertionPoint is topLevelKeyEndBoundary's counterpart for a key
// that does not exist yet: insert before a trailing "..." marker, else at
// true EOF.
func documentInsertionPoint(lines []string) int {
	if marker := documentEndMarkerLine(lines); marker > 0 {
		return marker
	}
	return len(lines) + 1
}

// documentEndMarkerLine returns the 1-indexed line number of a bare "..."
// document-end marker line, or 0 if none exists.
func documentEndMarkerLine(lines []string) int {
	for i, line := range lines {
		if strings.TrimSpace(strings.TrimRight(line, "\r\n")) == "..." {
			return i + 1
		}
	}
	return 0
}

// splitLinesKeepEnds splits text into physical lines, each retaining its
// own original line terminator exactly (including none, for a final
// unterminated line, and "\r\n" verbatim rather than normalising it) — so
// line N here always corresponds exactly to yaml.Node.Line == N on the same
// text.
func splitLinesKeepEnds(text string) []string {
	if text == "" {
		return nil
	}
	var lines []string
	start := 0
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			lines = append(lines, text[start:i+1])
			start = i + 1
		}
	}
	if start < len(text) {
		lines = append(lines, text[start:])
	}
	return lines
}

// spliceLines returns lines with [startLine, endLineExclusive) (1-indexed)
// replaced by insertLines — or, when startLine == endLineExclusive, with
// insertLines inserted at that point without removing anything. It never
// mutates lines. If the line immediately before the insertion point lacks a
// terminator (the file did not end with a newline), one is added first, so
// the inserted content never lands on the same physical line as what came
// before it.
func spliceLines(lines []string, startLine, endLineExclusive int, insertLines []string, newline string) []string {
	before := append([]string(nil), lines[:startLine-1]...)
	after := lines[endLineExclusive-1:]
	if n := len(before); n > 0 && len(insertLines) > 0 {
		last := before[n-1]
		if !strings.HasSuffix(last, "\n") {
			before[n-1] = last + newline
		}
	}
	result := make([]string, 0, len(before)+len(insertLines)+len(after))
	result = append(result, before...)
	result = append(result, insertLines...)
	result = append(result, after...)
	return result
}

func joinLines(lines []string) string {
	return strings.Join(lines, "")
}

func lineEndingOf(text string) string {
	if strings.Contains(text, "\r\n") {
		return "\r\n"
	}
	return "\n"
}

func convertNewlines(text, newline string) string {
	if newline == "\n" {
		return text
	}
	return strings.ReplaceAll(text, "\n", newline)
}

// renderKeyValuePair renders exactly the one key/value pair as its own
// two-space-indented YAML block (ending with a single "\n"), so it can be
// spliced into an existing file without re-encoding — and thereby
// reformatting — anything else in it.
func renderKeyValuePair(key, value *yaml.Node) (string, error) {
	temporaryRoot := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{key, value}}
	temporaryDocument := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{temporaryRoot}}
	var buffer bytes.Buffer
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	if err := encoder.Encode(temporaryDocument); err != nil {
		return "", fmt.Errorf("render peers block: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return "", fmt.Errorf("render peers block: %w", err)
	}
	return buffer.String(), nil
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

// parsePeersUpstream decodes peers.upstream from raw YAML bytes and
// validates it the same way SetPeersUpstream validates a value before
// writing it, so LoadPeersUpstream and verifySpliceResult apply exactly one
// rule between them.
func parsePeersUpstream(raw []byte) (PeersUpstreamConfig, bool, error) {
	var file peersConfigFile
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return PeersUpstreamConfig{}, false, fmt.Errorf("parse config: %w", err)
	}
	if file.Peers == nil || file.Peers.Upstream == nil {
		return PeersUpstreamConfig{}, false, nil
	}
	upstream := *file.Peers.Upstream
	if err := validatePeersUpstreamURL(upstream.URL); err != nil {
		return PeersUpstreamConfig{}, false, fmt.Errorf("peers.upstream.url: %w", err)
	}
	if !filepath.IsAbs(upstream.TokenFile) {
		return PeersUpstreamConfig{}, false, fmt.Errorf("peers.upstream.token_file must be an absolute path, got %q", upstream.TokenFile)
	}
	return upstream, true, nil
}

// LoadPeersUpstream reads peers.upstream from path. found is false when the
// file, the peers section, or the upstream section is absent — the laptop
// simply has no configured hub yet, which is not an error.
func LoadPeersUpstream(path string) (PeersUpstreamConfig, bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return PeersUpstreamConfig{}, false, nil
	}
	if err != nil {
		return PeersUpstreamConfig{}, false, fmt.Errorf("read config %s: %w", path, err)
	}
	upstream, found, err := parsePeersUpstream(raw)
	if err != nil {
		return PeersUpstreamConfig{}, false, fmt.Errorf("config %s: %w", path, err)
	}
	return upstream, found, nil
}
