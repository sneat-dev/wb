package agentguard

import (
	"strings"
)

// This file holds a deliberately small shell reader.
//
// It is not a shell parser and must never grow into one. A general parser
// would have to model expansion, substitution, functions, and arithmetic, and
// every gap in it becomes either a missed violation or — far worse — a
// legitimate command wrongly refused. What is modelled here is exactly what a
// conservative guard needs: where one command ends and the next begins, which
// words are quoted, which words are redirection targets, and where a heredoc
// body starts and stops so its contents are never mistaken for commands.
//
// Everything it cannot model resolves to "no finding", which is an allow.

// segment is one simple command in a command line.
type segment struct {
	// Words are the command's words with one level of quoting removed,
	// excluding redirection operators and their targets.
	Words []string
	// RedirectTargets are the files this command writes to via >, >>, and
	// friends, as written.
	RedirectTargets []string
	// Separator is the operator that ENDED the previous segment, so a reader
	// can tell `cd x && cmd` (sequential, shares a working directory) from
	// `cmd | other` (a pipeline).
	Separator string

	// The fields below are read only by the masked-pipeline policy
	// (pipeline.go); every other policy ignores them and keeps reading
	// Words, RedirectTargets and Separator exactly as before.

	// Scope lists the ( ) and { } groups and $( ) substitutions the command
	// sits in, outermost first.
	Scope []*scopeFrame
	// Piped reports that this command's output feeds a pipe directly. A
	// command inside a group whose closing is piped is reported by that
	// group's scopeFrame.Piped instead.
	Piped bool
	// Substitutions are the bodies of the backtick and double-quoted $( )
	// substitutions in the command's words. The reader keeps scanning those
	// words as text, so Words is unchanged; the bodies are here so a policy
	// can read them as commands of their own.
	Substitutions []string
	// Behind reports that the command follows && or || (also across a line
	// break), so it only runs when an earlier command allows it. Separator
	// says the same for a command on the same line, but a line break
	// replaces it.
	Behind bool
	// Background reports that the command is followed by a lone &, so it runs
	// in a child shell.
	Background bool
}

// scopeFrame is one open ( ) or { } group, or $( ) substitution.
type scopeFrame struct {
	// Subshell is set when the group runs in a child shell, so an option set
	// inside it is gone after the closing parenthesis. A brace group runs in
	// the current shell and leaves its options in place.
	Subshell bool
	// Group is set for a plain ( ) or { } group, whose exit status is its last
	// command's and so is hidden when the group is piped. A substitution's
	// output is captured, not piped.
	Group bool
	// Conditional is set when the group began behind &&, ||, a pipe, or inside
	// a conditional group, or is a function body, so whether it runs at all is
	// not known.
	Conditional bool
	// Piped is set when the closing of the group is followed by a pipe, as in
	// `{ cmd; } 2>&1 | tail`.
	Piped bool
	// Background is set when the closing of the group is followed by a lone &.
	Background bool
}

// splitSegments breaks a command line into simple commands.
//
// Quoting is honoured so an operator inside a quoted string never splits, and
// heredoc bodies are skipped entirely so a Python script embedded in one is
// never read as shell.
func splitSegments(command string) []segment {
	reader := &shellReader{input: command}
	return reader.read()
}

type shellReader struct {
	input string
	index int

	segments []segment
	current  segment
	word     strings.Builder
	hasWord  bool

	// heredocDelimiters queues the terminators of heredocs opened on the
	// current line; their bodies begin after the next newline.
	heredocDelimiters []string

	// stack is the open groups, outermost first.
	stack []*scopeFrame
	// closed is the group whose closing was the last thing read, so a pipe
	// that comes next (after redirections only) pipes the whole group.
	closed *scopeFrame
	// substitutions are the bodies found in the current command's words.
	substitutions []string
	// inBacktick is set between an opening backtick and its closing one.
	inBacktick bool
	// quotedSubstitutionEnd is the index just past where the double-quoted
	// substitution recorded last closes. Up to it the text is that
	// substitution's body, which a policy reads on its own as a command line
	// (so a heredoc inside it is skipped and a real nested substitution is
	// found), so a backtick or $( ) met on the way is prose or a copy of
	// something the body already holds and is not recorded again.
	quotedSubstitutionEnd int
}

func (r *shellReader) read() []segment {
	for r.index < len(r.input) {
		character := r.input[r.index]
		switch character {
		case '\\':
			r.readEscape()
		case '\'':
			r.readSingleQuoted()
		case '"':
			r.readDoubleQuoted()
		case '\n':
			r.index++
			if r.pipePending() {
				// `a |` then a newline continues the pipeline: the next line
				// is still the other end of the pipe.
				r.consumeHeredocBodies()
				continue
			}
			behind := r.behindAcrossNewline()
			r.endSegment("\n")
			r.current.Behind = behind
			r.consumeHeredocBodies()
		case '<':
			r.readHeredocOrInput()
		case '>':
			r.readOutputRedirect()
		case '&', '|', ';':
			r.readOperator()
		case '(', ')':
			// A subshell boundary ends the current command. The grouping
			// itself carries no meaning most policies need; the masked-pipeline
			// policy reads it through Scope.
			r.readParenthesis(character)
		case '`':
			r.readBacktick()
		case '{', '}':
			// A brace is a group boundary only when it stands as its own
			// word (`{ gh pr merge 1; }`). Inside a word it is brace
			// expansion (`gh pr {merge,} 1`) or a parameter expansion
			// (`${X:-merge}`), and the word must survive intact so a gh
			// walker can read it (wb#500 fifth review, S2).
			if r.braceIsWord() {
				r.readBrace(character)
			} else {
				r.word.WriteByte(character)
				r.hasWord = true
				r.index++
			}
		case '#':
			// A comment starts only where a word could start (bash: "a word
			// beginning with #"). `echo a#b` is one word, and
			// `# use wb pr land; never gh pr merge` is not a command
			// (wb#500 fifth review, false refusal 1).
			if r.hasWord {
				r.word.WriteByte(character)
				r.hasWord = true
				r.index++
			} else {
				r.skipComment()
			}
		case ' ', '\t', '\r':
			r.index++
			r.endWord()
		default:
			r.word.WriteByte(character)
			r.hasWord = true
			r.index++
		}
	}
	r.endSegment("")
	return r.segments
}

// pipePending reports whether the reader is between a pipe operator and the
// command it feeds: nothing has been read since the operator.
func (r *shellReader) pipePending() bool {
	return !r.hasWord && len(r.current.Words) == 0 && len(r.current.RedirectTargets) == 0 &&
		(r.current.Separator == "|" || r.current.Separator == "|&")
}

func (r *shellReader) readEscape() {
	r.index++
	if r.index < len(r.input) {
		if r.input[r.index] == '\n' {
			// A line continuation joins the lines; it is not a word character.
			r.index++
			return
		}
		r.word.WriteByte(r.input[r.index])
		r.hasWord = true
		r.index++
	}
}

func (r *shellReader) readSingleQuoted() {
	r.index++
	r.hasWord = true
	for r.index < len(r.input) && r.input[r.index] != '\'' {
		r.word.WriteByte(r.input[r.index])
		r.index++
	}
	if r.index < len(r.input) {
		r.index++
	}
}

func (r *shellReader) readDoubleQuoted() {
	r.index++
	r.hasWord = true
	for r.index < len(r.input) && r.input[r.index] != '"' {
		if r.input[r.index] == '\\' && r.index+1 < len(r.input) {
			r.index++
			r.word.WriteByte(r.input[r.index])
			r.index++
			continue
		}
		r.recordQuotedSubstitution()
		r.word.WriteByte(r.input[r.index])
		r.index++
	}
	if r.index < len(r.input) {
		r.index++
	}
}

// recordQuotedSubstitution notes a $( ) or backtick substitution that starts at
// the reader's index inside double quotes, where the reader treats the text as
// part of one word and would otherwise never look inside it. Only the
// outermost one is recorded: its body is read on its own later, and that read
// finds what is nested in it.
func (r *shellReader) recordQuotedSubstitution() {
	if r.index < r.quotedSubstitutionEnd {
		return
	}
	rest := r.input[r.index:]
	switch {
	case strings.HasPrefix(rest, "$("):
		body := dollarParenBody(rest[2:])
		r.substitutions = append(r.substitutions, body)
		r.quotedSubstitutionEnd = r.index + 2 + len(body) + 1
	case rest[0] == '`':
		body := backtickBody(rest[1:])
		r.substitutions = append(r.substitutions, body)
		r.quotedSubstitutionEnd = r.index + 1 + len(body) + 1
	}
}

// readHeredocOrInput handles <, <<, <<-, and <<<. Only << and <<- open a body
// that must be skipped.
func (r *shellReader) readHeredocOrInput() {
	if strings.HasPrefix(r.input[r.index:], "<<<") {
		r.index += 3
		r.endWord()
		// A here-string's operand is data, not a redirection target.
		r.skipSpaces()
		r.readRawWord()
		return
	}
	if strings.HasPrefix(r.input[r.index:], "<<") {
		r.index += 2
		if r.index < len(r.input) && r.input[r.index] == '-' {
			r.index++
		}
		r.endWord()
		r.skipSpaces()
		delimiter := r.readRawWord()
		if delimiter != "" {
			r.heredocDelimiters = append(r.heredocDelimiters, delimiter)
		}
		return
	}
	// A plain input redirection reads a file; it never writes one.
	r.index++
	r.endWord()
	r.skipSpaces()
	r.readRawWord()
}

func (r *shellReader) readOutputRedirect() {
	r.index++
	if r.index < len(r.input) && r.input[r.index] == '>' {
		r.index++
	}
	if r.index < len(r.input) && (r.input[r.index] == '|' || r.input[r.index] == '&') {
		r.index++
	}
	// A file descriptor written just before the operator (2>, 1>>) is not a
	// command word.
	if r.hasWord && isAllDigits(r.word.String()) {
		r.word.Reset()
		r.hasWord = false
	}
	r.endWord()
	r.skipSpaces()
	target := r.readRawWord()
	if target != "" {
		r.current.RedirectTargets = append(r.current.RedirectTargets, target)
	}
}

func (r *shellReader) readOperator() {
	character := r.input[r.index]
	operator := string(character)
	r.index++
	if r.index < len(r.input) && r.input[r.index] == character && character != ';' {
		operator += string(character)
		r.index++
	} else if character == '|' && r.index < len(r.input) && r.input[r.index] == '&' {
		// `|&` pipes stderr as well as stdout; it is a pipe, not a pipe
		// followed by a background operator.
		operator = "|&"
		r.index++
	}
	// A lone & backgrounds the command rather than joining two; either way it
	// ends the command in front of it, which is all the reader needs.
	before := len(r.segments)
	r.endSegment(operator)
	r.current.Behind = operator == "&&" || operator == "||"
	if operator == "&" {
		switch {
		case len(r.segments) > before:
			r.segments[len(r.segments)-1].Background = true
		case r.closed != nil:
			r.closed.Background = true
		}
	}
	if operator == "|" || operator == "|&" {
		// The pipe takes the command in front of it, or, when only
		// redirections (`} 2>&1 |`) or nothing stand there, the group that
		// just closed.
		feeds := len(r.segments) > before && len(r.segments[len(r.segments)-1].Words) > 0
		if len(r.segments) > before {
			r.segments[len(r.segments)-1].Piped = true
		}
		if !feeds && r.closed != nil {
			r.closed.Piped = true
		}
	}
	r.closed = nil
}

// readParenthesis handles ( and ). A ( opens a subshell, or a substitution
// when a $, < or > comes right before it; a ) closes the innermost one.
func (r *shellReader) readParenthesis(character byte) {
	substitution := r.index > 0 && strings.IndexByte("$<>", r.input[r.index-1]) >= 0
	conditional := r.currentIsConditional()
	r.index++
	r.endSegment(string(character))
	if character == '(' {
		r.open(&scopeFrame{Subshell: true, Group: !substitution, Conditional: conditional})
		return
	}
	r.close(true)
}

// readBrace handles a { or } that stands as its own word.
func (r *shellReader) readBrace(character byte) {
	conditional := r.currentIsConditional()
	r.index++
	before := len(r.segments)
	function := r.closed != nil && r.closed.Subshell
	r.endSegment(string(character))
	if character == '{' {
		// `name() {` and `function name {` define a function: the body runs
		// when it is called, not here.
		function = function || (len(r.segments) > before && r.segments[before].Words[0] == "function")
		r.open(&scopeFrame{Group: true, Conditional: conditional || function})
		return
	}
	r.close(false)
}

// currentIsConditional reports whether the command being read only runs when
// an earlier one allows it: it follows &&, ||, or a pipe, or it is inside a
// group that does.
func (r *shellReader) currentIsConditional() bool {
	switch r.current.Separator {
	case "&&", "||", "|", "|&":
		return true
	}
	return r.current.Behind || (len(r.stack) > 0 && r.stack[len(r.stack)-1].Conditional)
}

// behindAcrossNewline reports whether the line break being read continues a
// command that ended in && or ||, so the next command still runs only when an
// earlier one allows it.
func (r *shellReader) behindAcrossNewline() bool {
	empty := !r.hasWord && len(r.current.Words) == 0 && len(r.current.RedirectTargets) == 0
	return empty && (r.current.Separator == "&&" || r.current.Separator == "||" || r.current.Behind)
}

func (r *shellReader) open(frame *scopeFrame) {
	r.stack = append(r.stack, frame)
	r.closed = nil
}

// close pops the innermost group of the matching kind. An unbalanced closing
// (a case pattern's ")") leaves the stack alone.
func (r *shellReader) close(subshell bool) {
	r.closed = nil
	if len(r.stack) == 0 {
		return
	}
	top := r.stack[len(r.stack)-1]
	if top.Subshell != subshell {
		return
	}
	r.stack = r.stack[:len(r.stack)-1]
	if top.Group {
		r.closed = top
	}
}

// readBacktick records the body of a backtick substitution. The backticks stay
// in the word and the body is still read as text, exactly as before; the
// recorded copy is for a policy that wants to read it as a command.
func (r *shellReader) readBacktick() {
	if r.index >= r.quotedSubstitutionEnd {
		if !r.inBacktick {
			r.substitutions = append(r.substitutions, backtickBody(r.input[r.index+1:]))
		}
		r.inBacktick = !r.inBacktick
	}
	r.word.WriteByte('`')
	r.hasWord = true
	r.index++
}

// backtickBody returns the text up to the next unescaped backtick.
func backtickBody(rest string) string {
	for index := 0; index < len(rest); index++ {
		switch rest[index] {
		case '\\':
			index++
		case '`':
			return rest[:index]
		}
	}
	return rest
}

// dollarParenBody returns the text of a $( ) substitution that starts at rest
// (just after the "$("), up to the matching parenthesis. A heredoc body in it
// is prose: a parenthesis there (a "1)" list item) does not count.
func dollarParenBody(rest string) string {
	depth := 1
	var delimiters []string
	for index := 0; index < len(rest); index++ {
		switch rest[index] {
		case '\\':
			index++
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return rest[:index]
			}
		case '<':
			delimiter, next := heredocOperand(rest, index)
			if delimiter != "" {
				delimiters = append(delimiters, delimiter)
			}
			index = next - 1
		case '\n':
			for _, delimiter := range delimiters {
				index = skipHeredocLines(rest, index+1, delimiter) - 1
			}
			delimiters = nil
		}
	}
	return rest
}

// heredocOperand reads the redirection that starts at rest[index], a <, and
// returns the delimiter of a heredoc (<< or <<-) with any quotes removed, and
// the index just past the operator and its word. The delimiter is empty for
// anything else, a here-string (<<<) and a plain < included.
func heredocOperand(rest string, index int) (string, int) {
	if strings.HasPrefix(rest[index:], "<<<") {
		return "", index + 3
	}
	if !strings.HasPrefix(rest[index:], "<<") {
		return "", index + 1
	}
	index += 2
	if index < len(rest) && rest[index] == '-' {
		index++
	}
	for index < len(rest) && (rest[index] == ' ' || rest[index] == '\t') {
		index++
	}
	start := index
	for index < len(rest) && strings.IndexByte(" \t\r\n;&|<>()", rest[index]) < 0 {
		index++
	}
	return strings.Trim(rest[start:index], `'"\`), index
}

// skipHeredocLines returns the index of the line after the one that holds
// delimiter, starting the search at from, or the end of rest.
func skipHeredocLines(rest string, from int, delimiter string) int {
	for from < len(rest) {
		end := strings.IndexByte(rest[from:], '\n')
		line := rest[from:]
		next := len(rest)
		if end >= 0 {
			line = rest[from : from+end]
			next = from + end + 1
		}
		if strings.TrimSpace(line) == delimiter {
			return next
		}
		from = next
	}
	return len(rest)
}

// readRawWord reads a single word, honouring quotes, without recording it as a
// command word. Used for redirection targets and heredoc delimiters.
func (r *shellReader) readRawWord() string {
	var builder strings.Builder
	for r.index < len(r.input) {
		character := r.input[r.index]
		if character == ' ' || character == '\t' || character == '\r' || character == '\n' ||
			character == ';' || character == '&' || character == '|' ||
			character == '<' || character == '>' || character == '(' || character == ')' {
			break
		}
		switch character {
		case '\'':
			r.index++
			for r.index < len(r.input) && r.input[r.index] != '\'' {
				builder.WriteByte(r.input[r.index])
				r.index++
			}
			if r.index < len(r.input) {
				r.index++
			}
		case '"':
			r.index++
			for r.index < len(r.input) && r.input[r.index] != '"' {
				builder.WriteByte(r.input[r.index])
				r.index++
			}
			if r.index < len(r.input) {
				r.index++
			}
		case '\\':
			r.index++
			if r.index < len(r.input) {
				builder.WriteByte(r.input[r.index])
				r.index++
			}
		default:
			builder.WriteByte(character)
			r.index++
		}
	}
	return builder.String()
}

// braceIsWord reports whether the { or } at the reader's index is a whole
// word: nothing of a word is pending in front of it and a word separator, an
// operator or the end of input follows it.
func (r *shellReader) braceIsWord() bool {
	if r.hasWord {
		return false
	}
	next := r.index + 1
	if next >= len(r.input) {
		return true
	}
	switch r.input[next] {
	case ' ', '\t', '\r', '\n', ';', '&', '|', ')', '(':
		return true
	}
	return false
}

// skipComment advances to the end of the line without consuming the newline,
// so the line still ends the segment and any heredoc bodies still follow it.
func (r *shellReader) skipComment() {
	for r.index < len(r.input) && r.input[r.index] != '\n' {
		r.index++
	}
}

func (r *shellReader) skipSpaces() {
	for r.index < len(r.input) && (r.input[r.index] == ' ' || r.input[r.index] == '\t') {
		r.index++
	}
}

// consumeHeredocBodies advances past every heredoc body queued on the line
// that just ended, so nothing inside one is read as a command.
func (r *shellReader) consumeHeredocBodies() {
	for _, delimiter := range r.heredocDelimiters {
		r.skipHeredocBody(delimiter)
	}
	r.heredocDelimiters = nil
}

func (r *shellReader) skipHeredocBody(delimiter string) {
	for r.index < len(r.input) {
		lineEnd := strings.IndexByte(r.input[r.index:], '\n')
		var line string
		if lineEnd < 0 {
			line = r.input[r.index:]
			r.index = len(r.input)
		} else {
			line = r.input[r.index : r.index+lineEnd]
			r.index += lineEnd + 1
		}
		if strings.TrimSpace(line) == delimiter {
			return
		}
	}
}

func (r *shellReader) endWord() {
	if !r.hasWord {
		return
	}
	word := r.word.String()
	r.word.Reset()
	r.hasWord = false
	r.current.Words = append(r.current.Words, word)
	r.closed = nil
}

func (r *shellReader) endSegment(separator string) {
	r.endWord()
	if len(r.current.Words) > 0 || len(r.current.RedirectTargets) > 0 {
		r.current.Scope = append([]*scopeFrame(nil), r.stack...)
		r.current.Substitutions = r.substitutions
		r.segments = append(r.segments, r.current)
	}
	r.substitutions = nil
	r.current = segment{Separator: separator}
}

func isAllDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
