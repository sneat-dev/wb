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

	// pendingRedirect is set once a redirection operator has been read, so
	// the next word is recorded as its target rather than as an argument.
	pendingRedirect bool

	// heredocDelimiters queues the terminators of heredocs opened on the
	// current line; their bodies begin after the next newline.
	heredocDelimiters []string
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
			r.endSegment("\n")
			r.consumeHeredocBodies()
		case '<':
			r.readHeredocOrInput()
		case '>':
			r.readOutputRedirect()
		case '&', '|', ';':
			r.readOperator()
		case '(', ')', '{', '}':
			// A subshell or group boundary ends the current command. The
			// grouping itself carries no meaning the guard needs.
			r.index++
			r.endSegment(string(character))
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
		r.word.WriteByte(r.input[r.index])
		r.index++
	}
	if r.index < len(r.input) {
		r.index++
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
	r.pendingRedirect = true
	r.skipSpaces()
	target := r.readRawWord()
	r.pendingRedirect = false
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
	}
	// A lone & backgrounds the command rather than joining two; either way it
	// ends the command in front of it, which is all the reader needs.
	r.endSegment(operator)
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
	if r.pendingRedirect {
		r.current.RedirectTargets = append(r.current.RedirectTargets, word)
		return
	}
	r.current.Words = append(r.current.Words, word)
}

func (r *shellReader) endSegment(separator string) {
	r.endWord()
	if len(r.current.Words) > 0 || len(r.current.RedirectTargets) > 0 {
		r.segments = append(r.segments, r.current)
	}
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
