package wbconfig

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
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
// silently duplicated. The replaced range ends at the last line the
// "peers:" value's own subtree actually occupies (nodeSubtreeEndLine), never
// at "the next top-level key" or "EOF": those are wrong whenever anything
// sits between the peers subtree and the next key that yaml.v3 does not
// attribute to that key's own starting line — a comment immediately above
// it, a blank line, a second "---" document, or a trailing comment block
// when peers is last. Getting this wrong previously deleted exactly that
// kind of content while still reporting success. Only the exact
// peers-subtree range (or, if peers is absent, an insertion point before a
// trailing "..." document-end marker, or at true EOF) is replaced; the rest
// of the original text is copied through byte for byte. Sibling keys
// already under "peers:" are preserved, not dropped, because the existing
// node holding them is mutated in place rather than replaced.
//
// A root document written in flow style ("{remote: ..., peers: ...}"), or
// one where "peers:" somehow shares a physical line with another top-level
// key, is refused outright before any line arithmetic runs: a single-line
// document has no safe per-key line range to replace.
//
// Before ever renaming the staged file into place, verifySpliceResult
// re-parses the result and refuses — leaving the original file completely
// untouched — if it does not decode back to exactly the url and token_file
// just written, or if anything outside the "peers:" key changed. It checks
// this two ways, deliberately without reusing splicePeersUpstream's own
// line-range logic, so a bug in that logic cannot hide itself from its own
// backstop: a structural, decode-based deep-equal of every other top-level
// key's value, and an independent, freshly-computed byte-for-byte compare of
// every comment and blank line outside the peers subtree (which the decode
// check cannot see at all, since comments carry no decoded value).
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
// (via parsePeersUpstream) and independently confirms nothing besides
// "peers:" changed, through two checks that share no line-range logic with
// splicePeersUpstream itself (see its doc comment for why that independence
// matters).
func verifySpliceResult(original, updated, hubURL, tokenFile string) error {
	upstream, found, err := parsePeersUpstream([]byte(updated))
	if err != nil {
		return fmt.Errorf("result does not parse: %w", err)
	}
	if !found || upstream.URL != hubURL || upstream.TokenFile != tokenFile {
		return fmt.Errorf("result peers.upstream = %+v (found=%t), want url=%q token_file=%q", upstream, found, hubURL, tokenFile)
	}

	// Structural check: decode both documents into plain values (not yaml.v3
	// Nodes, and not via any of splicePeersUpstream's own line-range
	// helpers) and require every key besides "peers" to be exactly equal.
	// This catches a changed or vanished key regardless of how the splice
	// computed its line range.
	beforeKeys, err := decodeTopLevelKeys(original)
	if err != nil {
		return fmt.Errorf("re-check original: %w", err)
	}
	afterKeys, err := decodeTopLevelKeys(updated)
	if err != nil {
		return fmt.Errorf("re-check result: %w", err)
	}
	delete(beforeKeys, "peers")
	delete(afterKeys, "peers")
	if !reflect.DeepEqual(beforeKeys, afterKeys) {
		return errors.New("a key other than peers changed")
	}

	// Textual check: a comment or a blank line carries no decoded value, so
	// the structural check above cannot see one go missing. Compare every
	// line outside the peers subtree byte for byte instead, computed fresh
	// from each document's own parse.
	beforeOutsidePeers, err := textOutsidePeersSubtree(original)
	if err != nil {
		return fmt.Errorf("re-check original: %w", err)
	}
	afterOutsidePeers, err := textOutsidePeersSubtree(updated)
	if err != nil {
		return fmt.Errorf("re-check result: %w", err)
	}
	// Trimmed only of trailing newline characters: inserting new content
	// after a file that did not itself end with one requires adding exactly
	// one newline first (new YAML content cannot start on the same physical
	// line as existing content) — a fix-up, not a content change — so it
	// must not itself trip this refusal. Any other difference still does.
	if strings.TrimRight(beforeOutsidePeers, "\r\n") != strings.TrimRight(afterOutsidePeers, "\r\n") {
		return errors.New("a comment or blank line outside peers: changed")
	}
	return nil
}

// decodeTopLevelKeys decodes text's top-level keys into plain Go values (not
// yaml.v3 Nodes), for verifySpliceResult's structural, line-arithmetic-free
// comparison.
func decodeTopLevelKeys(text string) (map[string]any, error) {
	result := map[string]any{}
	if strings.TrimSpace(text) == "" {
		return result, nil
	}
	if err := yaml.Unmarshal([]byte(text), &result); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if result == nil {
		result = map[string]any{}
	}
	return result, nil
}

// textOutsidePeersSubtree returns text with the top-level "peers:" key's
// exact subtree range removed (or text unchanged if "peers:" is absent),
// using nodeSubtreeEndLine — the same narrow, independently-auditable line
// primitive splicePeersUpstream uses, but computed here from a fresh parse
// of text rather than by calling into splicePeersUpstream's own boundary
// decision, so this function's correctness does not depend on the splice
// having chosen the same case (replace vs. insert, flow vs. block) it
// actually ran.
func textOutsidePeersSubtree(text string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return text, nil
	}
	_, root, err := parseYAMLDocument(text)
	if err != nil {
		return "", err
	}
	index := findTopLevelKeyIndex(root, "peers")
	if index < 0 {
		return text, nil
	}
	if err := refuseIfPeersSpliceUnsafe(root, index); err != nil {
		return "", err
	}
	lines := splitLinesKeepEnds(text)
	startLine := root.Content[index].Line
	endLineExclusive := nodeSubtreeEndLine(root.Content[index+1]) + 1
	return joinLines(spliceLines(lines, startLine, endLineExclusive, nil, lineEndingOf(text))), nil
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
	if err := refuseIfPeersSpliceUnsafe(root, peersKeyIndex); err != nil {
		return "", err
	}

	var peersKey, peersValue *yaml.Node
	var existingSubtreeEndLine int
	if peersKeyIndex >= 0 {
		peersKey = root.Content[peersKeyIndex]
		peersValue = root.Content[peersKeyIndex+1]
		// Captured before any mutation below: once setMappingScalar/append
		// touch peersValue's Content, a freshly-constructed child node (the
		// new "upstream:" value) has no Line of its own, so this must be
		// computed from the pristine, just-parsed original tree.
		existingSubtreeEndLine = nodeSubtreeEndLine(peersValue)
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
		endLineExclusive := existingSubtreeEndLine + 1
		return joinLines(spliceLines(lines, startLine, endLineExclusive, renderedLines, newline)), nil
	}
	insertionLine := documentInsertionPoint(lines)
	return joinLines(spliceLines(lines, insertionLine, insertionLine, renderedLines, newline)), nil
}

// refuseIfPeersSpliceUnsafe reports an error when the document's shape makes
// line-range splicing unsafe: a root written in flow style ("{a: 1, b: 2}"),
// where every key can share one physical line and there is no per-key line
// range to isolate at all, or — belt and suspenders, since this should only
// be reachable given a flow-style root — "peers:" literally sharing its
// start line with another top-level key. peersKeyIndex may be -1 (peers does
// not exist yet); the flow-style check still applies to the insert path,
// since inserting new block-style lines into what may render back out as a
// single physical line is just as unsafe.
func refuseIfPeersSpliceUnsafe(root *yaml.Node, peersKeyIndex int) error {
	if root.Style&yaml.FlowStyle != 0 {
		return errors.New("peers.upstream cannot be safely edited while the top-level document is written in flow style (\"{...}\"); rewrite it in block style first")
	}
	if peersKeyIndex < 0 {
		return nil
	}
	peersLine := root.Content[peersKeyIndex].Line
	for i := 0; i+1 < len(root.Content); i += 2 {
		if i == peersKeyIndex {
			continue
		}
		if root.Content[i].Line == peersLine {
			return errors.New("peers.upstream cannot be safely edited while \"peers:\" shares a line with another top-level key")
		}
	}
	return nil
}

// nodeSubtreeEndLine returns the highest source line node's own subtree
// reaches: node's own starting line, plus any newlines embedded in a
// multi-line scalar's decoded value, and the same computed recursively for
// every child. It is the exact end of a key's own value in the original
// source — never "the next key's line" or "EOF" — so a splice or a
// verification check never reaches past that value into a comment, a blank
// line, a second YAML document, or a key that merely happens to follow it.
func nodeSubtreeEndLine(node *yaml.Node) int {
	if node == nil {
		return 0
	}
	end := node.Line
	if node.Kind == yaml.ScalarNode {
		end += strings.Count(node.Value, "\n")
	}
	for _, child := range node.Content {
		if childEnd := nodeSubtreeEndLine(child); childEnd > end {
			end = childEnd
		}
	}
	return end
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

// documentInsertionPoint is where a "peers:" key that does not exist yet is
// inserted: before a trailing "..." document-end marker (which "puts" any
// text appended after it into a second YAML document, invisible to a
// single-document parse), else at true EOF.
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
