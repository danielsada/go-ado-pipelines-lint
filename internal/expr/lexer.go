// Package expr implements the Azure Pipelines expression language: lexing, parsing,
// scanning strings for ${{ }}, $[ ] and $( ) spans, template directives and evaluation
// with support for unknown values (used for constant folding).
package expr

import (
	"fmt"
	"strings"
)

// TokenKind is the kind of a lexical token.
type TokenKind int

// Token kinds.
const (
	TokEOF TokenKind = iota
	TokString
	TokNumber
	TokVersion
	TokIdent
	TokDot
	TokComma
	TokLParen
	TokRParen
	TokLBracket
	TokRBracket
	TokStar
)

// Token is a lexical token. Pos is the byte offset in the expression.
type Token struct {
	Kind  TokenKind
	Text  string
	Value string
	Pos   int
}

// SyntaxError is an expression syntax error at a byte offset.
type SyntaxError struct {
	Pos int
	Msg string
}

func (e *SyntaxError) Error() string { return fmt.Sprintf("%s (at offset %d)", e.Msg, e.Pos) }

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9') || c == '-'
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// Lex splits an expression into tokens.
func Lex(s string) ([]Token, error) {
	var toks []Token
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '\'':
			tok, n, err := lexString(s, i)
			if err != nil {
				return nil, err
			}
			toks = append(toks, tok)
			i = n
		case isDigit(c) || ((c == '-' || c == '.') && i+1 < len(s) && isDigit(s[i+1])):
			tok, n := lexNumber(s, i)
			toks = append(toks, tok)
			i = n
		case isIdentStart(c):
			j := i + 1
			for j < len(s) && isIdentPart(s[j]) {
				j++
			}
			toks = append(toks, Token{Kind: TokIdent, Text: s[i:j], Value: s[i:j], Pos: i})
			i = j
		default:
			k, ok := punct[c]
			if !ok {
				return nil, &SyntaxError{Pos: i, Msg: fmt.Sprintf("unexpected character %q", c)}
			}
			toks = append(toks, Token{Kind: k, Text: string(c), Pos: i})
			i++
		}
	}
	return append(toks, Token{Kind: TokEOF, Pos: len(s)}), nil
}

var punct = map[byte]TokenKind{
	'.': TokDot, ',': TokComma, '(': TokLParen, ')': TokRParen,
	'[': TokLBracket, ']': TokRBracket, '*': TokStar,
}

func lexString(s string, start int) (Token, int, error) {
	var b strings.Builder
	i := start + 1
	for i < len(s) {
		if s[i] != '\'' {
			b.WriteByte(s[i])
			i++
			continue
		}
		if i+1 < len(s) && s[i+1] == '\'' {
			b.WriteByte('\'')
			i += 2
			continue
		}
		return Token{Kind: TokString, Text: s[start : i+1], Value: b.String(), Pos: start}, i + 1, nil
	}
	return Token{}, 0, &SyntaxError{Pos: start, Msg: "unterminated string literal"}
}

func lexNumber(s string, start int) (Token, int) {
	j := start
	if s[j] == '-' {
		j++
	}
	dots := 0
	for j < len(s) && (isDigit(s[j]) || s[j] == '.') {
		if s[j] == '.' {
			dots++
		}
		j++
	}
	kind := TokNumber
	if dots >= 2 {
		kind = TokVersion
	}
	return Token{Kind: kind, Text: s[start:j], Value: s[start:j], Pos: start}, j
}
