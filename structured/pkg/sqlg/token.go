package sqlg

import (
	"fmt"
	"strings"
)

// TokenType is the type of a lexical token.
type TokenType int

const (
	TokEOF TokenType = iota
	TokWord
	TokNumber
	TokString
	TokIdent // quoted identifier
	TokPunct
	TokOp
	TokComment
	// TokUnknown mirrors sqlglot's TokenType.UNKNOWN: one unrecognized character.
	// It is in parser.ID_VAR_TOKENS (so _parse_id_var accepts its text); elsewhere it fails parsing.
	TokUnknown
)

// Token is a lexical token.
type Token struct {
	Type TokenType
	Text string
	// QuoteChar records the original quoting character of a TokIdent.
	QuoteChar byte
	Pos       int
}

func (t Token) String() string { return fmt.Sprintf("%d:%q", t.Type, t.Text) }

// isKeyword reports whether the token is the given keyword (case-insensitive).
func (t Token) isKeyword(kw string) bool {
	return t.Type == TokWord && strings.EqualFold(t.Text, kw)
}

// LexError is a lexing error, mirroring sqlglot.errors.TokenizeError.
type LexError struct {
	Msg string
	Pos int
}

func (e *LexError) Error() string { return e.Msg }

// Lex splits SQL text into a token sequence (using default dialect semantics).
func Lex(sql string) ([]Token, error) { return LexDialect(sql, "") }

// LexDialect tokenizes according to the dialect. The key difference is quote-character
// class: IDENTIFIERS is checked before QUOTES (sqlglot tokens.py), so a character in
// both (e.g. `"` in T-SQL/SQLite) is an identifier quote.
func LexDialect(sql, dialect string) ([]Token, error) {
	l := &lexer{
		src:                 []rune(sql),
		doubleQuoteIsString: doubleQuoteIsString(dialect),
		backtickIsIdent:     backtickIsIdentifier(dialect),
	}
	return l.run()
}

// doubleQuoteIsString reports whether `"` is a string-literal quote in the dialect
// (i.e. not in sqlglot's IDENTIFIERS).
func doubleQuoteIsString(dialect string) bool {
	switch dialect {
	case "bigquery", "databricks", "doris", "hive", "mysql", "prql",
		"singlestore", "solr", "spark", "spark2", "starrocks", "tableau":
		return true
	}
	return false
}

// backtickIsIdentifier reports whether backtick is an identifier quote in the dialect;
// elsewhere it lexes as UNKNOWN and parsing fails.
func backtickIsIdentifier(dialect string) bool {
	switch dialect {
	case "athena", "bigquery", "clickhouse", "databricks", "doris", "drill",
		"hive", "mysql", "prql", "singlestore", "solr", "spark", "spark2",
		"sqlite", "starrocks":
		return true
	}
	return false
}

type lexer struct {
	src []rune
	pos int
	// `"` is a string literal, not an identifier quote.
	doubleQuoteIsString bool
	// When false, backticks degrade to single-char UNKNOWN tokens so the parser
	// reports the same ParseError as sqlglot.
	backtickIsIdent bool
}

func (l *lexer) run() ([]Token, error) {
	var out []Token
	for {
		l.skipSpaceAndComments()
		if l.pos >= len(l.src) {
			break
		}
		start := l.pos
		c := l.src[l.pos]
		switch {
		case c == '\'' || (c == '"' && l.doubleQuoteIsString):
			tok, err := l.readQuoted(c, c, c)
			if err != nil {
				return nil, err
			}
			tok.Pos = start
			out = append(out, tok)
		case c == '"':
			tok, err := l.readQuoted('"', '"', 0)
			if err != nil {
				return nil, err
			}
			tok.Type = TokIdent
			tok.Pos = start
			out = append(out, tok)
		case c == '`' && l.backtickIsIdent:
			tok, err := l.readQuoted('`', '`', 0)
			if err != nil {
				return nil, err
			}
			tok.Type = TokIdent
			tok.Pos = start
			out = append(out, tok)
		case c == '`':
			// Dialects without backtick identifiers emit UNKNOWN per single character
			// (sqlglot SINGLE_TOKENS); token offsets determine error Col positions downstream.
			out = append(out, Token{Type: TokUnknown, Text: "`", Pos: start})
			l.pos++
		case c == '[':
			// T-SQL bracket identifier; treated as a quote only if it can be closed with ].
			tok, err := l.readBracketIdent()
			if err == nil {
				tok.Pos = start
				out = append(out, tok)
			} else {
				l.pos = start
				l.pos++
				out = append(out, Token{Type: TokPunct, Text: "[", Pos: start})
			}
		case c >= '0' && c <= '9':
			out = append(out, l.readNumber())
		case isWordStart(c):
			out = append(out, l.readWord())
		default:
			out = append(out, l.readPunctOrOp())
		}
	}
	out = append(out, Token{Type: TokEOF, Pos: len(l.src)})
	return out, nil
}

func (l *lexer) skipSpaceAndComments() {
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v' {
			l.pos++
			continue
		}
		if c == '-' && l.pos+1 < len(l.src) && l.src[l.pos+1] == '-' {
			for l.pos < len(l.src) && l.src[l.pos] != '\n' {
				l.pos++
			}
			continue
		}
		if c == '#' {
			for l.pos < len(l.src) && l.src[l.pos] != '\n' {
				l.pos++
			}
			continue
		}
		if c == '/' && l.pos+1 < len(l.src) && l.src[l.pos+1] == '*' {
			l.pos += 2
			for l.pos+1 < len(l.src) && !(l.src[l.pos] == '*' && l.src[l.pos+1] == '/') {
				l.pos++
			}
			if l.pos+1 < len(l.src) {
				l.pos += 2
			} else {
				l.pos = len(l.src)
			}
			continue
		}
		break
	}
}

// readQuoted reads quoted content, supporting doubled-quote escaping and backslash escaping.
func (l *lexer) readQuoted(open, closeCh, esc rune) (Token, error) {
	openPos := l.pos
	l.pos++ // skip the opening quote
	var sb strings.Builder
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		if c == '\\' && l.pos+1 < len(l.src) {
			nx := l.src[l.pos+1]
			// MySQL-style escapes
			switch nx {
			case 'n':
				sb.WriteRune('\n')
			case 't':
				sb.WriteRune('\t')
			case 'r':
				sb.WriteRune('\r')
			case '0':
				sb.WriteRune(0)
			case '\\', '\'', '"', '`':
				sb.WriteRune(nx)
			default:
				sb.WriteRune('\\')
				sb.WriteRune(nx)
			}
			l.pos += 2
			continue
		}
		if c == closeCh {
			if l.pos+1 < len(l.src) && l.src[l.pos+1] == closeCh {
				sb.WriteRune(closeCh)
				l.pos += 2
				continue
			}
			l.pos++
			return Token{Type: TokString, Text: sb.String(), QuoteChar: byte(open)}, nil
		}
		sb.WriteRune(c)
		l.pos++
	}
	return Token{}, &LexError{Msg: fmt.Sprintf("Missing closing quote for %q", string(open)), Pos: openPos}
}

// readBracketIdent reads a T-SQL [ident], where ]] is the escape.
func (l *lexer) readBracketIdent() (Token, error) {
	start := l.pos
	l.pos++
	var sb strings.Builder
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		if c == ']' {
			if l.pos+1 < len(l.src) && l.src[l.pos+1] == ']' {
				sb.WriteRune(']')
				l.pos += 2
				continue
			}
			l.pos++
			return Token{Type: TokIdent, Text: sb.String(), QuoteChar: '['}, nil
		}
		sb.WriteRune(c)
		l.pos++
	}
	return Token{}, &LexError{Msg: "Missing closing bracket", Pos: start}
}

func (l *lexer) readNumber() Token {
	start := l.pos
	for l.pos < len(l.src) && l.src[l.pos] >= '0' && l.src[l.pos] <= '9' {
		l.pos++
	}
	if l.pos < len(l.src) && l.src[l.pos] == '.' {
		l.pos++
		for l.pos < len(l.src) && l.src[l.pos] >= '0' && l.src[l.pos] <= '9' {
			l.pos++
		}
	}
	if l.pos < len(l.src) && (l.src[l.pos] == 'e' || l.src[l.pos] == 'E') {
		save := l.pos
		l.pos++
		if l.pos < len(l.src) && (l.src[l.pos] == '+' || l.src[l.pos] == '-') {
			l.pos++
		}
		if l.pos < len(l.src) && l.src[l.pos] >= '0' && l.src[l.pos] <= '9' {
			for l.pos < len(l.src) && l.src[l.pos] >= '0' && l.src[l.pos] <= '9' {
				l.pos++
			}
		} else {
			l.pos = save
		}
	}
	return Token{Type: TokNumber, Text: string(l.src[start:l.pos]), Pos: start}
}

func (l *lexer) readWord() Token {
	start := l.pos
	for l.pos < len(l.src) && isWordPart(l.src[l.pos]) {
		l.pos++
	}
	return Token{Type: TokWord, Text: string(l.src[start:l.pos]), Pos: start}
}

// readPunctOrOp reads punctuation/operators, preferring two-character operators.
func (l *lexer) readPunctOrOp() Token {
	start := l.pos
	two := ""
	if l.pos+1 < len(l.src) {
		two = string(l.src[l.pos : l.pos+2])
	}
	switch two {
	case ">=", "<=", "<>", "!=", "||", "::", "->", ":=", "=>", "<=>":
		l.pos += 2
		return Token{Type: TokOp, Text: two, Pos: start}
	}
	if two == "->" && l.pos+2 < len(l.src) && l.src[l.pos+2] == '>' {
		l.pos += 3
		return Token{Type: TokOp, Text: "->>", Pos: start}
	}
	c := l.src[l.pos]
	l.pos++
	switch c {
	case '(', ')', ',', '.', ';', '*', '=', '<', '>', '+', '-', '/', '%', ':', '?', '&', '|', '^', '~', '!':
		t := TokPunct
		if strings.ContainsRune("=<>+-/%&|^~!", c) {
			t = TokOp
		}
		return Token{Type: t, Text: string(c), Pos: start}
	}
	return Token{Type: TokPunct, Text: string(c), Pos: start}
}

func isWordStart(c rune) bool {
	return c == '_' || c == '$' || c == '@' || c == '#' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
		c > 127
}

func isWordPart(c rune) bool {
	return isWordStart(c) || (c >= '0' && c <= '9')
}
