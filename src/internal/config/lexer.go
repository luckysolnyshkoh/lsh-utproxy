package config

import (
	"bufio"
	"io"
	"strings"
	"unicode"
)

type TokenType int

const (
	TokenEOF TokenType = iota
	TokenWord
)

type Token struct {
	Type        TokenType
	Value       string
	Line        int
	Indented    bool // true if the line this token starts on had leading whitespace
	FirstInLine bool // true if this is the first token on its line
}

type Lexer struct {
	scanner *bufio.Scanner
	line    int
	pending []Token
}

func NewLexer(r io.Reader) *Lexer {
	sc := bufio.NewScanner(r)
	const maxTokenSize = 1024 * 1024
	sc.Buffer(make([]byte, maxTokenSize), maxTokenSize)
	sc.Split(bufio.ScanLines)
	return &Lexer{scanner: sc, line: 0}
}

func (l *Lexer) Next() Token {
	if len(l.pending) > 0 {
		t := l.pending[0]
		l.pending = l.pending[1:]
		return t
	}
	for {
		if !l.scanner.Scan() {
			return Token{Type: TokenEOF, Line: l.line}
		}
		l.line++
		raw := l.scanner.Text()
		if l.line == 1 {
			// Windows editors (Notepad, PowerShell Out-File) add a UTF-8 BOM;
			// without stripping it the first keyword of the file would not match.
			raw = strings.TrimPrefix(raw, "\ufeff")
		}
		trimmed, _ := stripComment(raw)
		if strings.TrimSpace(trimmed) == "" {
			continue
		}
		indented := len(trimmed) > 0 && (trimmed[0] == ' ' || trimmed[0] == '\t')
		s := strings.TrimSpace(trimmed)
		words := splitWords(s)
		first := true
		for _, w := range words {
			if w == "" {
				continue
			}
			l.pending = append(l.pending, Token{
				Type:        TokenWord,
				Value:       w,
				Line:        l.line,
				Indented:    indented,
				FirstInLine: first,
			})
			first = false
		}
		if len(l.pending) > 0 {
			t := l.pending[0]
			l.pending = l.pending[1:]
			return t
		}
	}
}

func stripComment(line string) (string, bool) {
	inQuote := false
	quote := byte(0)
	for i := 0; i < len(line); i++ {
		c := line[i]
		if !inQuote && (c == '"' || c == '\'') {
			inQuote = true
			quote = c
			continue
		}
		if inQuote && c == quote {
			inQuote = false
			quote = 0
			continue
		}
		if !inQuote && c == '#' {
			return line[:i], true
		}
	}
	return line, false
}

func splitWords(s string) []string {
	var words []string
	var cur strings.Builder
	inQuote := false
	quote := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		// Backslash only escapes inside quotes, and only for the quote
		// character itself or another backslash. Everywhere else it is a plain
		// character, so Windows paths (C:\Soft\lsh-utproxy) survive intact.
		if inQuote && c == '\\' && i+1 < len(s) {
			next := s[i+1]
			if next == quote || next == '\\' {
				cur.WriteByte(next)
				i++
				continue
			}
			cur.WriteByte(c)
			continue
		}
		if !inQuote && (c == '"' || c == '\'') {
			inQuote = true
			quote = c
			cur.WriteByte(c)
			continue
		}
		if inQuote && c == quote {
			inQuote = false
			quote = 0
			cur.WriteByte(c)
			continue
		}
		if !inQuote && (unicode.IsSpace(rune(c)) || c == ',' || c == '\t') {
			if cur.Len() > 0 {
				words = append(words, cur.String())
				cur.Reset()
			}
			continue
		}
		cur.WriteByte(c)
	}
	if cur.Len() > 0 {
		words = append(words, cur.String())
	}
	return words
}

func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
