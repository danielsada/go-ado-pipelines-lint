package expr

import (
	"fmt"
	"regexp"
	"strings"
)

// SpanKind is the kind of an embedded expression.
type SpanKind int

// Span kinds.
const (
	// SpanTemplate is a compile-time expression: ${{ ... }}.
	SpanTemplate SpanKind = iota
	// SpanRuntime is a runtime expression: $[ ... ].
	SpanRuntime
	// SpanMacro is a macro variable reference: $(name).
	SpanMacro
)

// Span is an embedded expression inside a string. Start/End are byte offsets of the whole
// span (End exclusive); Expr is the inner text, starting at byte offset ExprStart.
type Span struct {
	Kind      SpanKind
	Start     int
	End       int
	Expr      string
	ExprStart int
}

// ScanError is an unterminated expression.
type ScanError struct {
	Kind SpanKind
	Pos  int
	Msg  string
}

var macroRe = regexp.MustCompile(`^\$\(([A-Za-z0-9_][A-Za-z0-9_.\-]*)\)`)

// Scan finds all ${{ }}, $[ ] and $( ) spans in s.
func Scan(s string) ([]Span, []ScanError) {
	var spans []Span
	var errs []ScanError
	for i := 0; i < len(s); i++ {
		if s[i] != '$' || i+1 >= len(s) {
			continue
		}
		switch {
		case strings.HasPrefix(s[i:], "${{"):
			end := findClose(s, i+3, "}}")
			if end < 0 {
				errs = append(errs, ScanError{Kind: SpanTemplate, Pos: i, Msg: "unterminated template expression: missing '}}'"})
				return spans, errs
			}
			spans = append(spans, Span{Kind: SpanTemplate, Start: i, End: end + 2, Expr: s[i+3 : end], ExprStart: i + 3})
			i = end + 1
		case s[i+1] == '[':
			end := findBracket(s, i+2)
			if end < 0 {
				errs = append(errs, ScanError{Kind: SpanRuntime, Pos: i, Msg: "unterminated runtime expression: missing ']'"})
				continue
			}
			spans = append(spans, Span{Kind: SpanRuntime, Start: i, End: end + 1, Expr: s[i+2 : end], ExprStart: i + 2})
			i = end
		case s[i+1] == '(':
			if m := macroRe.FindStringSubmatch(s[i:]); m != nil {
				spans = append(spans, Span{Kind: SpanMacro, Start: i, End: i + len(m[0]), Expr: m[1], ExprStart: i + 2})
				i += len(m[0]) - 1
			}
		}
	}
	return spans, errs
}

// findClose returns the index of closer at or after start, skipping single-quoted strings.
func findClose(s string, start int, closer string) int {
	inStr := false
	for j := start; j < len(s); j++ {
		if s[j] == '\'' {
			inStr = !inStr
			continue
		}
		if !inStr && strings.HasPrefix(s[j:], closer) {
			return j
		}
	}
	return -1
}

func findBracket(s string, start int) int {
	inStr := false
	depth := 1
	for j := start; j < len(s); j++ {
		c := s[j]
		switch {
		case c == '\'':
			inStr = !inStr
		case inStr:
		case c == '[':
			depth++
		case c == ']':
			depth--
			if depth == 0 {
				return j
			}
		}
	}
	return -1
}

// HasTemplate reports whether s contains a ${{ expression.
func HasTemplate(s string) bool { return strings.Contains(s, "${{") }

// HasDynamic reports whether s contains any expression or macro syntax.
func HasDynamic(s string) bool {
	return strings.Contains(s, "${{") || strings.Contains(s, "$[") || strings.Contains(s, "$(")
}

// Whole returns the single span covering all of s (ignoring surrounding whitespace).
func Whole(s string, kind SpanKind) (Span, bool) {
	spans, errs := Scan(s)
	if len(errs) > 0 || len(spans) != 1 || spans[0].Kind != kind {
		return Span{}, false
	}
	sp := spans[0]
	if strings.TrimSpace(s[:sp.Start]) != "" || strings.TrimSpace(s[sp.End:]) != "" {
		return Span{}, false
	}
	return sp, true
}

// DirectiveKind is the kind of a ${{ }} mapping key.
type DirectiveKind int

// Directive kinds.
const (
	DirExpr DirectiveKind = iota
	DirIf
	DirElseIf
	DirElse
	DirEach
	DirInsert
)

// Directive is a parsed ${{ }} mapping key.
type Directive struct {
	Kind DirectiveKind
	// Cond is the condition of if/elseif.
	Cond Node
	// Var and Coll are the loop variable and collection of each.
	Var  string
	Coll Node
	// Expr is the key expression for DirExpr.
	Expr Node
	// Text is the inner text of the directive, and Offset its byte offset in the key.
	Text   string
	Offset int
}

// IsDirective reports whether key is a ${{ }} key.
func IsDirective(key string) bool {
	_, ok := Whole(key, SpanTemplate)
	return ok
}

// ParseDirective parses a mapping key of the form ${{ ... }}.
// It returns (nil, nil) if key is not such a key.
func ParseDirective(key string) (*Directive, error) {
	sp, ok := Whole(key, SpanTemplate)
	if !ok {
		return nil, nil
	}
	inner := strings.TrimSpace(sp.Expr)
	off := sp.ExprStart + strings.Index(sp.Expr, inner)
	d := &Directive{Text: inner, Offset: off}
	word, rest := splitWord(inner)
	var err error
	switch word {
	case "if", "elseif":
		d.Kind = DirIf
		if word == "elseif" {
			d.Kind = DirElseIf
		}
		if strings.TrimSpace(rest) == "" {
			return d, &SyntaxError{Pos: off, Msg: word + " requires a condition"}
		}
		d.Cond, err = Parse(rest)
	case "else":
		d.Kind = DirElse
		if strings.TrimSpace(rest) != "" {
			return d, &SyntaxError{Pos: off, Msg: "else takes no condition (use elseif)"}
		}
	case "insert":
		d.Kind = DirInsert
	case "each":
		d.Kind = DirEach
		err = parseEach(d, rest, off)
	default:
		d.Kind = DirExpr
		d.Expr, err = Parse(inner)
	}
	return d, err
}

func splitWord(s string) (word, rest string) {
	i := strings.IndexAny(s, " \t\n(")
	if i < 0 {
		return s, ""
	}
	return s[:i], s[i:]
}

var eachRe = regexp.MustCompile(`^\s*([A-Za-z_][A-Za-z0-9_]*)\s+in\s+(.+)$`)

func parseEach(d *Directive, rest string, off int) error {
	m := eachRe.FindStringSubmatch(rest)
	if m == nil {
		return &SyntaxError{Pos: off, Msg: "each must have the form 'each <name> in <expression>'"}
	}
	d.Var = m[1]
	coll, err := Parse(m[2])
	if err != nil {
		return fmt.Errorf("in each collection: %w", err)
	}
	d.Coll = coll
	return nil
}
