package expr

import (
	"fmt"
	"strconv"
	"strings"
)

// Node is an expression AST node.
type Node interface {
	// Offset returns the byte offset of the node in the source expression.
	Offset() int
}

// Literal is a constant value.
type Literal struct {
	Val Value
	Pos int
}

// Ident is a named value such as parameters, variables, dependencies or a loop variable.
type Ident struct {
	Name string
	Pos  int
}

// Member is property dereference: X.Name.
type Member struct {
	X    Node
	Name string
	Pos  int
}

// Index is index access: X[Index].
type Index struct {
	X     Node
	Index Node
	Pos   int
}

// Call is a function call.
type Call struct {
	Name string
	Args []Node
	Pos  int
}

// Offset implements Node.
func (n *Literal) Offset() int { return n.Pos }

// Offset implements Node.
func (n *Ident) Offset() int { return n.Pos }

// Offset implements Node.
func (n *Member) Offset() int { return n.Pos }

// Offset implements Node.
func (n *Index) Offset() int { return n.Pos }

// Offset implements Node.
func (n *Call) Offset() int { return n.Pos }

type parser struct {
	toks []Token
	i    int
}

// Parse parses a complete expression.
func Parse(s string) (Node, error) {
	toks, err := Lex(s)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	if p.peek().Kind == TokEOF {
		return nil, &SyntaxError{Pos: 0, Msg: "empty expression"}
	}
	n, err := p.expr()
	if err != nil {
		return nil, err
	}
	if t := p.peek(); t.Kind != TokEOF {
		return nil, &SyntaxError{Pos: t.Pos, Msg: fmt.Sprintf("unexpected %q", t.Text)}
	}
	return n, nil
}

func (p *parser) peek() Token { return p.toks[p.i] }

func (p *parser) next() Token {
	t := p.toks[p.i]
	if t.Kind != TokEOF {
		p.i++
	}
	return t
}

func (p *parser) expect(k TokenKind, what string) (Token, error) {
	t := p.next()
	if t.Kind != k {
		got := t.Text
		if t.Kind == TokEOF {
			got = "end of expression"
		}
		return t, &SyntaxError{Pos: t.Pos, Msg: fmt.Sprintf("expected %s, found %q", what, got)}
	}
	return t, nil
}

func (p *parser) expr() (Node, error) {
	n, err := p.primary()
	if err != nil {
		return nil, err
	}
	if _, isLit := n.(*Literal); isLit {
		return n, nil
	}
	return p.postfix(n)
}

func (p *parser) primary() (Node, error) {
	t := p.next()
	switch t.Kind {
	case TokString:
		return &Literal{Val: Str(t.Value), Pos: t.Pos}, nil
	case TokNumber:
		f, err := strconv.ParseFloat(t.Value, 64)
		if err != nil {
			return nil, &SyntaxError{Pos: t.Pos, Msg: fmt.Sprintf("invalid number %q", t.Text)}
		}
		return &Literal{Val: Num(f), Pos: t.Pos}, nil
	case TokVersion:
		if _, ok := parseVersion(t.Value); !ok {
			return nil, &SyntaxError{Pos: t.Pos, Msg: fmt.Sprintf("invalid version %q", t.Text)}
		}
		return &Literal{Val: Version(t.Value), Pos: t.Pos}, nil
	case TokIdent:
		return p.ident(t)
	case TokEOF:
		return nil, &SyntaxError{Pos: t.Pos, Msg: "unexpected end of expression"}
	}
	return nil, &SyntaxError{Pos: t.Pos, Msg: fmt.Sprintf("unexpected %q", t.Text)}
}

func (p *parser) ident(t Token) (Node, error) {
	if p.peek().Kind == TokLParen {
		p.next()
		args, err := p.args()
		if err != nil {
			return nil, err
		}
		return &Call{Name: t.Value, Args: args, Pos: t.Pos}, nil
	}
	switch strings.ToLower(t.Value) {
	case "true":
		return &Literal{Val: Bool(true), Pos: t.Pos}, nil
	case "false":
		return &Literal{Val: Bool(false), Pos: t.Pos}, nil
	case "null":
		return &Literal{Val: Null(), Pos: t.Pos}, nil
	}
	return &Ident{Name: t.Value, Pos: t.Pos}, nil
}

func (p *parser) args() ([]Node, error) {
	var args []Node
	if p.peek().Kind == TokRParen {
		p.next()
		return args, nil
	}
	for {
		a, err := p.expr()
		if err != nil {
			return nil, err
		}
		args = append(args, a)
		t := p.next()
		if t.Kind == TokRParen {
			return args, nil
		}
		if t.Kind != TokComma {
			return nil, &SyntaxError{Pos: t.Pos, Msg: fmt.Sprintf("expected ',' or ')', found %q", t.Text)}
		}
	}
}

func (p *parser) postfix(n Node) (Node, error) {
	for {
		switch p.peek().Kind {
		case TokDot:
			dot := p.next()
			t := p.next()
			if t.Kind != TokIdent && t.Kind != TokStar {
				return nil, &SyntaxError{Pos: t.Pos, Msg: "expected property name after '.'"}
			}
			n = &Member{X: n, Name: t.Text, Pos: dot.Pos}
		case TokLBracket:
			lb := p.next()
			if p.peek().Kind == TokStar {
				p.next()
				if _, err := p.expect(TokRBracket, "']'"); err != nil {
					return nil, err
				}
				n = &Member{X: n, Name: "*", Pos: lb.Pos}
				continue
			}
			idx, err := p.expr()
			if err != nil {
				return nil, err
			}
			if _, err := p.expect(TokRBracket, "']'"); err != nil {
				return nil, err
			}
			n = &Index{X: n, Index: idx, Pos: lb.Pos}
		default:
			return n, nil
		}
	}
}

// Walk calls fn for n and every descendant, depth-first. If fn returns false, children are skipped.
func Walk(n Node, fn func(Node) bool) {
	if n == nil || !fn(n) {
		return
	}
	switch x := n.(type) {
	case *Member:
		Walk(x.X, fn)
	case *Index:
		Walk(x.X, fn)
		Walk(x.Index, fn)
	case *Call:
		for _, a := range x.Args {
			Walk(a, fn)
		}
	}
}

// Path returns the dotted access path for chains like parameters.a['b'].c, as
// ["parameters","a","b","c"]. ok is false if the chain contains non-literal indexes or calls.
func Path(n Node) (path []string, ok bool) {
	switch x := n.(type) {
	case *Ident:
		return []string{x.Name}, true
	case *Member:
		p, ok := Path(x.X)
		if !ok {
			return nil, false
		}
		return append(p, x.Name), true
	case *Index:
		p, ok := Path(x.X)
		lit, isLit := x.Index.(*Literal)
		if !ok || !isLit || lit.Val.Kind != KString {
			return p, false
		}
		return append(p, lit.Val.S), true
	}
	return nil, false
}

// Format renders an expression AST back to source form (normalized).
func Format(n Node) string {
	switch x := n.(type) {
	case *Literal:
		return x.Val.Literal()
	case *Ident:
		return x.Name
	case *Member:
		return Format(x.X) + "." + x.Name
	case *Index:
		return Format(x.X) + "[" + Format(x.Index) + "]"
	case *Call:
		parts := make([]string, len(x.Args))
		for i, a := range x.Args {
			parts[i] = Format(a)
		}
		return x.Name + "(" + strings.Join(parts, ", ") + ")"
	}
	return ""
}
