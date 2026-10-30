package config

import (
	"strings"
	"testing"
)

func tokenValues(l *Lexer) []Token {
	var out []Token
	for {
		t := l.Next()
		if t.Type == TokenEOF {
			return out
		}
		out = append(out, t)
	}
}

func TestLexerKeepsWindowsPaths(t *testing.T) {
	src := "global\n  log C:\\Soft\\lsh-utproxy\\logs\\lsh-utproxy.log format json level info\n"
	toks := tokenValues(NewLexer(strings.NewReader(src)))
	if len(toks) != 7 {
		t.Fatalf("tokens = %d (%v)", len(toks), toks)
	}
	want := `C:\Soft\lsh-utproxy\logs\lsh-utproxy.log`
	if toks[2].Value != want {
		t.Errorf("log target token = %q, want %q", toks[2].Value, want)
	}
	if !toks[2].Indented || toks[2].FirstInLine {
		t.Errorf("flags wrong for %q: indented=%v firstInLine=%v", toks[2].Value, toks[2].Indented, toks[2].FirstInLine)
	}
	if toks[0].FirstInLine != true || toks[0].Indented != false {
		t.Errorf("section header flags wrong: %+v", toks[0])
	}
}

func TestLexerSkipsUTF8BOM(t *testing.T) {
	// Notepad and PowerShell write a BOM; the first section header must still be
	// recognized.
	src := "\uFEFF" + "global\n  maxconn 100\n"
	cfg, err := NewParser(strings.NewReader(src)).Config()
	if err != nil {
		t.Fatalf("parse with BOM: %v", err)
	}
	if cfg.Global.MaxConn != 100 {
		t.Errorf("global section after a BOM was not parsed: %+v", cfg.Global)
	}
}

func TestLexerQuotesAndComments(t *testing.T) {
	src := `listen x
  bind "*:80"   # inline comment
  desc "a \"quoted\" value"
`
	toks := tokenValues(NewLexer(strings.NewReader(src)))
	got := make([]string, 0, len(toks))
	for _, tk := range toks {
		got = append(got, tk.Value)
	}
	want := []string{"listen", "x", "bind", `"*:80"`, "desc", `"a "quoted" value"`}
	if len(got) != len(want) {
		t.Fatalf("tokens = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("token %d = %q, want %q", i, got[i], want[i])
		}
	}
	if unquote(`"a "quoted" value"`) != `a "quoted" value` {
		t.Errorf("unquote failed: %q", unquote(`"a "quoted" value"`))
	}
}
