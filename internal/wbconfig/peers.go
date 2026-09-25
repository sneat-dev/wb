package wbconfig

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/sneat-dev/wb/internal/filewrite"
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
// Round 3 and round 4 each tried to derive the exact line range an existing
// "peers:" key occupies from decoded YAML structure (a node's line number, or
// the newlines embedded in a decoded scalar's value) and each time a
// different construction — a multi-line quoted scalar, a folded plain
// scalar, a block scalar — made that derived range wrong in one direction or
// the other: too short (leaking a sibling's lines into the replacement and
// duplicating them) or too long (deleting a second document, a trailing
// comment block, or a comment that merely happens to sit nearby), sometimes
// even producing a range past the end of the file's own line count.
//
// Round 5's answer is to stop trying to handle arbitrary YAML. This function
// supports exactly two shapes and refuses everything else outright, leaving
// the file completely untouched:
//
//   - Shape A: no top-level "peers:" key exists yet. A new block is appended
//     — before a trailing "---" or "..." document marker if the file has
//     one, otherwise at EOF.
//   - Shape B: an existing block-style "peers:" key sits at column 0
//     (matched by the literal, exact line "peers:" — no quoting, no
//     trailing comment, no unusual spacing), and every line of its body, up
//     to the first line whose first character is not a space, is either
//     blank or a single-line "key: value" pair (a single-line plain or
//     single-line quoted scalar, no escape sequences) — with at most one
//     nested sub-block, "upstream:", whose own children follow the exact
//     same single-line-scalar rule one level deeper. The block's extent is
//     found with nothing but a plain line scan (isPeersBodyLine,
//     scanPeersBlockEnd): never from a decoded node's line number or a
//     decoded scalar's length, so a construction that confused the old
//     line-range derivation cannot confuse this one — it simply fails to
//     match this narrow grammar and falls through to the refusal below.
//
// Anything else — a flow-style root or a flow-style peers: value, a
// multi-line quoted or plain scalar anywhere in the body, a block scalar, an
// anchor, alias or tag, a comment inside the body or immediately above
// "peers:", more than one "upstream:" child, or a body that fails to parse
// as YAML at all — is refused with a clear, actionable message rather than
// guessed at.
//
// verifyPeersUpstreamEdit then re-parses the result independently of every
// line-scan decision splicePeersBody made — decoding every YAML document
// (not just the first) and requiring every one but the first to be
// unchanged, decoding the first document's top-level keys and every
// peers: child besides "upstream" for a structural deep-equal, and
// separately re-running the same plain line scan against both the original
// and the updated text to require every byte outside the block's own range
// to be identical — before the staged file is ever renamed into place. Any
// mismatch refuses and leaves the original file untouched.
func SetPeersUpstream(path, hubURL, tokenFile string) error {
	return setPeersUpstreamInjected(path, hubURL, tokenFile, nil)
}

// setPeersUpstreamInjected is SetPeersUpstream's test seam (task-9 PR-8):
// every production call site reaches it only through SetPeersUpstream, which
// always passes a nil *filewrite.Injector, so production behaviour is
// unchanged. A test passes its own Injector to reach the create/chmod/
// write/sync/close/rename failure branches deterministically.
func setPeersUpstreamInjected(path, hubURL, tokenFile string, inj *filewrite.Injector) error {
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

	updated, err := rewritePeersUpstream(original, hubURL, tokenFile)
	if err != nil {
		return fmt.Errorf("update config %s: %w", path, err)
	}
	if err := verifyPeersUpstreamEdit(original, updated, hubURL, tokenFile); err != nil {
		return fmt.Errorf("refusing to write config %s: %w", path, err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	temporary, err := filewrite.CreateTemp(filepath.Dir(path), ".wb-config-*.yaml", inj)
	if err != nil {
		return fmt.Errorf("stage config: %w", err)
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := filewrite.ChmodFile(temporary, 0o600, temporaryName, inj); err != nil {
		_ = filewrite.Close(temporary, temporaryName, inj)
		return fmt.Errorf("protect staged config: %w", err)
	}
	if err := filewrite.Write(temporary, []byte(updated), temporaryName, inj); err != nil {
		_ = filewrite.Close(temporary, temporaryName, inj)
		return fmt.Errorf("write config: %w", err)
	}
	if err := filewrite.Sync(temporary, temporaryName, inj); err != nil {
		_ = filewrite.Close(temporary, temporaryName, inj)
		return fmt.Errorf("sync config: %w", err)
	}
	if err := filewrite.Close(temporary, temporaryName, inj); err != nil {
		return fmt.Errorf("close config: %w", err)
	}
	if err := filewrite.Rename(temporaryName, path, inj); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}

// errUnsupportedPeersShape is rewritePeersUpstream's one refusal error,
// returned for every document shape besides the two SetPeersUpstream's own
// doc comment names.
func errUnsupportedPeersShape(hubURL, tokenFile string) error {
	return fmt.Errorf("wb.yaml's peers: block uses YAML this editor does not rewrite; set peers.upstream by hand to url=%q token_file=%q", hubURL, tokenFile)
}

// rewritePeersUpstream is SetPeersUpstream's core. See SetPeersUpstream's own
// doc comment for the two shapes it supports and why everything else is
// refused rather than guessed at.
func rewritePeersUpstream(original, hubURL, tokenFile string) (string, error) {
	refusal := errUnsupportedPeersShape(hubURL, tokenFile)
	newline := lineEndingOf(original)

	if strings.TrimSpace(original) == "" {
		return appendNewPeersBlock(nil, newline, hubURL, tokenFile), nil
	}

	rootIsBlockMapping, peersExists, peersSupported := inspectPeersDocumentShape(original)
	if !rootIsBlockMapping {
		return "", refusal
	}
	if !peersExists {
		return appendNewPeersBlock(splitLinesKeepEnds(original), newline, hubURL, tokenFile), nil
	}
	if !peersSupported {
		return "", refusal
	}

	lines := splitLinesKeepEnds(original)
	keyLine, found := findExactPeersKeyLine(lines)
	if !found {
		return "", refusal
	}
	if keyLine > 0 && isCommentLine(lines[keyLine-1]) {
		return "", refusal
	}
	bodyEnd := scanPeersBlockEnd(lines, keyLine+1, len(lines), 1)

	body, err := parsePeersBody(lines, keyLine, bodyEnd)
	if err != nil {
		return "", refusal
	}

	return spliceUpstreamBlock(lines, bodyEnd, body, newline, hubURL, tokenFile), nil
}

// inspectPeersDocumentShape decodes original with yaml.v3's Node API purely
// to answer three structural questions that a plain line scan cannot: is the
// root document a block-style mapping at all, does a top-level "peers" key
// exist, and — if so — is its value a shape splicePeersUpstream's line scan
// can safely edit (a block-style mapping, or YAML null/absent). This is the
// only place decoded Node information is consulted; nothing here is ever
// used to compute a line range — findExactPeersKeyLine and
// scanPeersBlockEnd do that from the raw text alone.
func inspectPeersDocumentShape(original string) (rootIsBlockMapping, peersExists, peersSupported bool) {
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(original), &document); err != nil || len(document.Content) == 0 {
		return false, false, false
	}
	root := document.Content[0]
	if root.Kind != yaml.MappingNode || root.Style&yaml.FlowStyle != 0 {
		return false, false, false
	}
	index := findMappingKeyIndex(root, "peers")
	if index < 0 {
		return true, false, false
	}
	value := root.Content[index+1]
	switch {
	case value.Kind == yaml.ScalarNode && (value.Tag == "!!null" || value.Value == ""):
		return true, true, true
	case value.Kind == yaml.MappingNode && value.Style&yaml.FlowStyle == 0:
		return true, true, true
	default:
		return true, true, false
	}
}

func findMappingKeyIndex(mapping *yaml.Node, key string) int {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return i
		}
	}
	return -1
}

// stripLineEnding removes a trailing "\r\n" or "\n" (and a lone trailing
// "\r", for robustness) from one line, leaving its content only.
func stripLineEnding(line string) string {
	return strings.TrimRight(line, "\r\n")
}

func isCommentLine(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(stripLineEnding(line)), "#")
}

// leadingSpaceCount counts a line's leading ' ' characters. A tab is not a
// space and stops the count immediately, which is deliberate: YAML
// indentation with tabs is invalid, and isPeersBodyLine's own tab check
// refuses a body containing one rather than silently miscounting it.
func leadingSpaceCount(s string) int {
	n := 0
	for n < len(s) && s[n] == ' ' {
		n++
	}
	return n
}

// findExactPeersKeyLine returns the 0-indexed line that reads exactly
// "peers:" (its line ending stripped) — no quoting, no trailing
// whitespace or comment, no leading indent — or (-1, false) if no line
// matches. A "peers" key that decodes correctly but is spelled, quoted,
// commented or indented any other way is a real key inspectPeersDocument
// Shape can see, which rewritePeersUpstream must then refuse rather than
// silently duplicate by treating it as absent.
func findExactPeersKeyLine(lines []string) (int, bool) {
	for i, line := range lines {
		if stripLineEnding(line) == "peers:" {
			return i, true
		}
	}
	return -1, false
}

// scanPeersBlockEnd returns the first 0-indexed line in [start, limit) whose
// content, once its line ending is stripped, is non-blank and has fewer
// than minIndent leading spaces — purely a property of that line's own
// text, never of any decoded value's length. Blank lines never end the
// scan (they are valid body content); a document marker ("---", "...") or
// any other column-0-or-shallower line always does, without needing its
// own special case, because it necessarily has fewer leading spaces than
// any minIndent of at least 1.
func scanPeersBlockEnd(lines []string, start, limit, minIndent int) int {
	for i := start; i < limit; i++ {
		content := stripLineEnding(lines[i])
		if strings.TrimSpace(content) == "" {
			continue
		}
		if leadingSpaceCount(content) < minIndent {
			return i
		}
	}
	return limit
}

// peersBody is what parsePeersBody found while validating an existing
// peers: block's body against the narrow grammar SetPeersUpstream's doc
// comment describes. It carries only what spliceUpstreamBlock needs to
// place the new upstream: block — never any sibling's key or value, which
// stays byte-identical in the original text because nothing here ever
// reads it for any purpose but validation.
type peersBody struct {
	// innerIndent is the leading-space count peers' own immediate children
	// sit at, or -1 if the body has no children at all (an empty or
	// null-only "peers:").
	innerIndent int
	// hasUpstream and upstreamLine/upstreamBodyEnd describe an existing
	// "upstream:" child's exact line range — [upstreamLine, upstreamBodyEnd)
	// — covering a single inline line, a bare/null line, or a bare line plus
	// its own nested single-line-scalar children, whichever it is.
	hasUpstream     bool
	upstreamLine    int
	upstreamBodyEnd int
}

var errUnsupportedPeersBody = errors.New("unsupported peers: body")

// parsePeersBody validates every line of the peers: block's body — lines
// keyLine+1 up to (not including) bodyEnd — against the grammar
// SetPeersUpstream's doc comment describes, using nothing but each line's
// own leading-space count and content. It refuses (errUnsupportedPeersBody)
// on the first line that does not match: a different indent than the
// body's own established level, a tab, a comment, a value that is not a
// single-line plain or single-line quoted scalar with no escape sequences,
// a second "upstream:" child, or a non-"upstream:" child that itself opens
// a nested block.
func parsePeersBody(lines []string, keyLine, bodyEnd int) (peersBody, error) {
	body := peersBody{innerIndent: -1, upstreamLine: -1}
	i := keyLine + 1
	for i < bodyEnd {
		content := stripLineEnding(lines[i])
		if strings.TrimSpace(content) == "" {
			i++
			continue
		}
		if strings.ContainsRune(content, '\t') {
			return peersBody{}, errUnsupportedPeersBody
		}
		indent := leadingSpaceCount(content)
		if body.innerIndent == -1 {
			body.innerIndent = indent
		}
		if indent != body.innerIndent {
			return peersBody{}, errUnsupportedPeersBody
		}
		key, _, bare, ok := parseSimplePeersLine(content[indent:])
		if !ok {
			return peersBody{}, errUnsupportedPeersBody
		}
		if key == "upstream" {
			if body.hasUpstream {
				return peersBody{}, errUnsupportedPeersBody
			}
			body.hasUpstream = true
			body.upstreamLine = i
			if !bare {
				body.upstreamBodyEnd = i + 1
				i++
				continue
			}
			nestedEnd := scanPeersBlockEnd(lines, i+1, bodyEnd, indent+1)
			if _, err := parseFlatPeersScalars(lines, i+1, nestedEnd); err != nil {
				return peersBody{}, err
			}
			body.upstreamBodyEnd = nestedEnd
			i = nestedEnd
			continue
		}
		if bare {
			// Only "upstream:" may open a nested block; any other bare key
			// followed by more-indented lines is refused rather than
			// guessed at.
			if scanPeersBlockEnd(lines, i+1, bodyEnd, indent+1) > i+1 {
				return peersBody{}, errUnsupportedPeersBody
			}
		}
		i++
	}
	return body, nil
}

// parseFlatPeersScalars validates lines [start, end) as upstream's own
// children: the same single-line-scalar grammar, one consistent indent, but
// with no further nesting permitted at all (a bare key here — including a
// stray "upstream:" typo one level too deep — is refused).
func parseFlatPeersScalars(lines []string, start, end int) ([]string, error) {
	var keys []string
	innerIndent := -1
	for i := start; i < end; i++ {
		content := stripLineEnding(lines[i])
		if strings.TrimSpace(content) == "" {
			continue
		}
		if strings.ContainsRune(content, '\t') {
			return nil, errUnsupportedPeersBody
		}
		indent := leadingSpaceCount(content)
		if innerIndent == -1 {
			innerIndent = indent
		}
		if indent != innerIndent {
			return nil, errUnsupportedPeersBody
		}
		key, _, bare, ok := parseSimplePeersLine(content[indent:])
		if !ok || bare {
			return nil, errUnsupportedPeersBody
		}
		keys = append(keys, key)
	}
	return keys, nil
}

// parseSimplePeersLine matches one indent-stripped, line-ending-stripped
// body line against "key:" (bare, nothing after the colon) or
// "key: value" where value is a single-line plain or single-line quoted
// scalar with no escape sequences. ok is false for anything else — no
// colon, an invalid key name, no space after the colon, a comment anywhere
// in the value, or a value using YAML syntax (a block scalar, a flow
// collection, an anchor, an alias, a tag) this grammar deliberately does
// not support.
func parseSimplePeersLine(content string) (key, value string, bare, ok bool) {
	colon := strings.IndexByte(content, ':')
	if colon < 0 || !isSimplePeersKeyName(content[:colon]) {
		return "", "", false, false
	}
	key = content[:colon]
	rest := content[colon+1:]
	if rest == "" {
		return key, "", true, true
	}
	if rest[0] != ' ' {
		return "", "", false, false
	}
	value = strings.TrimSpace(rest)
	if value == "" {
		return key, "", true, true
	}
	if !isSafePeersScalarValue(value) {
		return "", "", false, false
	}
	return key, value, false, true
}

func isSimplePeersKeyName(key string) bool {
	if key == "" {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_':
		case i > 0 && ((c >= '0' && c <= '9') || c == '-' || c == '.'):
		default:
			return false
		}
	}
	return true
}

// isSafePeersScalarValue reports whether value (already trimmed) is a
// single-line plain scalar or a single-line quoted scalar with no escape
// sequences and no interior quote character — deliberately conservative:
// a block scalar indicator, a flow-collection bracket, an anchor, an
// alias, a tag, or a comment anywhere in the value is refused rather than
// interpreted.
func isSafePeersScalarValue(value string) bool {
	if strings.ContainsAny(value, "#\t") {
		return false
	}
	if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
		inner := value[1 : len(value)-1]
		return !strings.ContainsRune(inner, rune(value[0])) && !strings.ContainsRune(inner, '\\')
	}
	if strings.ContainsAny(value[:1], "\"'|>{}[]&*!%@`,") {
		return false
	}
	return !strings.Contains(value, ": ") && !strings.HasSuffix(value, ":")
}

// appendNewPeersBlock is shape A: lines may be nil (an empty or absent
// file). The new block is inserted before a trailing "---" or "..."
// document marker if one exists (anything after it belongs to a second
// document a single-document edit must never touch), otherwise at EOF.
func appendNewPeersBlock(lines []string, newline, hubURL, tokenFile string) string {
	insertAt := documentInsertionPoint(lines)
	block := []string{
		"peers:" + newline,
		"  upstream:" + newline,
		"    url: " + hubURL + newline,
		"    token_file: " + tokenFile + newline,
	}
	return joinLines(spliceLines(lines, insertAt, insertAt, block, newline))
}

// documentInsertionPoint returns the 0-indexed line to insert new top-level
// content before: the first "---" or "..." marker line after line 0 (a
// marker AT line 0 only opens the file's own single document and is not a
// second one), or len(lines) for true EOF.
func documentInsertionPoint(lines []string) int {
	for i, line := range lines {
		if i == 0 {
			continue
		}
		trimmed := strings.TrimSpace(stripLineEnding(line))
		if trimmed == "---" || trimmed == "..." {
			return i
		}
	}
	return len(lines)
}

// spliceUpstreamBlock is shape B's edit: replace exactly body's existing
// "upstream:" child's line range, or — if it has none yet — insert a new
// one as the body's last child (at bodyEnd, which is correct whether the
// body is empty or already has other children). Every other line, in the
// peers: block or outside it, is untouched.
func spliceUpstreamBlock(lines []string, bodyEnd int, body peersBody, newline, hubURL, tokenFile string) string {
	indent := body.innerIndent
	if indent < 0 {
		indent = 2
	}
	inner := strings.Repeat(" ", indent)
	deeper := strings.Repeat(" ", indent+2)
	block := []string{
		inner + "upstream:" + newline,
		deeper + "url: " + hubURL + newline,
		deeper + "token_file: " + tokenFile + newline,
	}
	start, end := bodyEnd, bodyEnd
	if body.hasUpstream {
		start, end = body.upstreamLine, body.upstreamBodyEnd
	}
	return joinLines(spliceLines(lines, start, end, block, newline))
}

// splitLinesKeepEnds splits text into physical lines, each retaining its
// own original line terminator exactly (including none, for a final
// unterminated line, and "\r\n" verbatim rather than normalising it).
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

// spliceLines returns lines with the 0-indexed range [start, end) replaced
// by insert — or, when start == end, with insert inserted at that point
// without removing anything. It never mutates lines, and every index it is
// called with in this file is derived from scanPeersBlockEnd/
// documentInsertionPoint, which never return a value outside [0, len(lines)]
// — so this never runs past the slice either way. If the line immediately
// before the insertion point lacks a terminator (the file did not end with
// one), one is added first, so inserted content never lands on the same
// physical line as what came before it.
func spliceLines(lines []string, start, end int, insert []string, newline string) []string {
	before := append([]string(nil), lines[:start]...)
	after := lines[end:]
	if n := len(before); n > 0 && len(insert) > 0 {
		last := before[n-1]
		if !strings.HasSuffix(last, "\n") {
			before[n-1] = last + newline
		}
	}
	result := make([]string, 0, len(before)+len(insert)+len(after))
	result = append(result, before...)
	result = append(result, insert...)
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

// verifyPeersUpstreamEdit is SetPeersUpstream's pre-write safety check. It
// shares no line-range logic with rewritePeersUpstream's own decisions —
// the exact coupling that let round 4's verifier hide round 4's own bug
// from itself — and runs two independent checks instead: a structural,
// fully-decoded deep-equal (every document but the first, every top-level
// key but "peers", every peers: child but "upstream"), and a textual,
// freshly-line-scanned byte-for-byte compare of everything outside the
// peers: block's own range in each of original and updated, computed
// separately for each.
func verifyPeersUpstreamEdit(original, updated, hubURL, tokenFile string) error {
	upstream, found, err := parsePeersUpstream([]byte(updated))
	if err != nil {
		return fmt.Errorf("result does not parse: %w", err)
	}
	if !found || upstream.URL != hubURL || upstream.TokenFile != tokenFile {
		return fmt.Errorf("result peers.upstream = %+v (found=%t), want url=%q token_file=%q", upstream, found, hubURL, tokenFile)
	}

	originalDocs, err := decodeAllPeersDocuments(original)
	if err != nil {
		return fmt.Errorf("re-check original: %w", err)
	}
	updatedDocs, err := decodeAllPeersDocuments(updated)
	if err != nil {
		return fmt.Errorf("re-check result: %w", err)
	}
	if len(originalDocs) != len(updatedDocs) {
		return errors.New("the number of YAML documents changed")
	}
	for i := 1; i < len(originalDocs); i++ {
		if !reflect.DeepEqual(originalDocs[i], updatedDocs[i]) {
			return fmt.Errorf("document %d changed", i+1)
		}
	}

	originalFirst := firstPeersDocumentMap(originalDocs)
	updatedFirst := firstPeersDocumentMap(updatedDocs)
	if !reflect.DeepEqual(withoutPeersKey(originalFirst), withoutPeersKey(updatedFirst)) {
		return errors.New("a key other than peers changed")
	}
	if !reflect.DeepEqual(peersChildrenExceptUpstream(originalFirst["peers"]), peersChildrenExceptUpstream(updatedFirst["peers"])) {
		return errors.New("a peers: child other than upstream changed")
	}

	originalOutside, err := textOutsidePeersBlock(original)
	if err != nil {
		return fmt.Errorf("re-check original: %w", err)
	}
	updatedOutside, err := textOutsidePeersBlock(updated)
	if err != nil {
		return fmt.Errorf("re-check result: %w", err)
	}
	// Trimmed only of trailing newline characters: inserting new content
	// after a file that did not itself end with one requires adding exactly
	// one newline first (new YAML content cannot start on the same physical
	// line as existing content) — a fix-up, not a content change — so it
	// must not itself trip this refusal. Any other difference still does.
	if strings.TrimRight(originalOutside, "\r\n") != strings.TrimRight(updatedOutside, "\r\n") {
		return errors.New("text outside peers: changed")
	}
	return nil
}

// decodeAllPeersDocuments decodes every YAML document text holds (a plain
// file has exactly one; a "---"-or-"..."-separated stream may have more) —
// never just the first, which is round 4's own independence gap: a bug that
// only corrupts a second document was invisible to a verifier that decoded
// nothing past the first. An empty or blank text is one implicit empty
// document, matching parsePeersUpstream's own treatment of an absent file.
func decodeAllPeersDocuments(text string) ([]any, error) {
	if strings.TrimSpace(text) == "" {
		return []any{map[string]any{}}, nil
	}
	decoder := yaml.NewDecoder(strings.NewReader(text))
	var docs []any
	for {
		var doc any
		if err := decoder.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		docs = append(docs, doc)
	}
	if len(docs) == 0 {
		docs = append(docs, map[string]any{})
	}
	return docs, nil
}

func firstPeersDocumentMap(docs []any) map[string]any {
	if len(docs) == 0 {
		return map[string]any{}
	}
	if m, ok := docs[0].(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func withoutPeersKey(m map[string]any) map[string]any {
	result := make(map[string]any, len(m))
	for k, v := range m {
		if k != "peers" {
			result[k] = v
		}
	}
	return result
}

func peersChildrenExceptUpstream(peersValue any) map[string]any {
	m, ok := peersValue.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return withoutKey(m, "upstream")
}

func withoutKey(m map[string]any, key string) map[string]any {
	result := make(map[string]any, len(m))
	for k, v := range m {
		if k != key {
			result[k] = v
		}
	}
	return result
}

// textOutsidePeersBlock returns text with the top-level "peers:" block's
// exact range removed (or text unchanged if no line reads exactly
// "peers:"), using the same plain-text primitives rewritePeersUpstream
// itself uses (findExactPeersKeyLine, scanPeersBlockEnd) but invoked fresh
// against text's own, independent parse — so verifyPeersUpstreamEdit's
// correctness never depends on rewritePeersUpstream having chosen the same
// case (shape A, shape B, or a refusal) it actually ran.
func textOutsidePeersBlock(text string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return text, nil
	}
	lines := splitLinesKeepEnds(text)
	keyLine, found := findExactPeersKeyLine(lines)
	if !found {
		return text, nil
	}
	bodyEnd := scanPeersBlockEnd(lines, keyLine+1, len(lines), 1)
	return joinLines(spliceLines(lines, keyLine, bodyEnd, nil, lineEndingOf(text))), nil
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
// writing it, so LoadPeersUpstream and verifyPeersUpstreamEdit apply
// exactly one rule between them.
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
