package expr

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Context supplies named values (parameters, variables, loop variables, ...) for evaluation.
// Names are matched case-insensitively. Missing names evaluate to Unknown.
type Context struct {
	named map[string]Value
	// Status optionally supplies known results for zero-argument status functions.
	Status map[string]Value
}

// NewContext creates an empty context.
func NewContext() *Context { return &Context{named: map[string]Value{}} }

// Set binds a named value.
func (c *Context) Set(name string, v Value) { c.named[strings.ToLower(name)] = v }

// With returns a copy of the context with an additional binding.
func (c *Context) With(name string, v Value) *Context {
	n := NewContext()
	n.Status = c.Status
	for k, val := range c.named {
		n.named[k] = val
	}
	n.Set(name, v)
	return n
}

// Lookup returns the value bound to name.
func (c *Context) Lookup(name string) (Value, bool) {
	if c == nil {
		return Unknown(), false
	}
	v, ok := c.named[strings.ToLower(name)]
	return v, ok
}

// Eval evaluates an expression AST. Anything that depends on run-time state evaluates to Unknown.
func Eval(n Node, ctx *Context) Value {
	switch x := n.(type) {
	case *Literal:
		return x.Val
	case *Ident:
		if v, ok := ctx.Lookup(x.Name); ok {
			return v
		}
		return Unknown()
	case *Member:
		return member(Eval(x.X, ctx), x.Name)
	case *Index:
		return index(Eval(x.X, ctx), Eval(x.Index, ctx))
	case *Call:
		return call(x, ctx)
	}
	return Unknown()
}

func member(base Value, name string) Value {
	switch base.Kind {
	case KUnknown:
		return Unknown()
	case KObject:
		if v, ok := base.Get(name); ok {
			return v
		}
	}
	return Null()
}

func index(base, idx Value) Value {
	if !base.Known() || !idx.Known() {
		return Unknown()
	}
	switch base.Kind {
	case KObject:
		return member(base, idx.String())
	case KArray:
		f, ok := idx.toNumber()
		i := int(f)
		if ok && i >= 0 && i < len(base.Arr) {
			return base.Arr[i]
		}
	}
	return Null()
}

// Func describes a built-in function.
type Func struct {
	Name string
	Min  int
	// Max is the maximum number of arguments, or -1 for unbounded.
	Max int
	// Status is true for job status check functions (always, succeeded, ...).
	Status bool
	fn     func(args []Value) Value
}

var funcs = map[string]*Func{}

func reg(name string, min, max int, fn func([]Value) Value) {
	funcs[strings.ToLower(name)] = &Func{Name: name, Min: min, Max: max, fn: fn}
}

func regStatus(name string, max int) {
	funcs[strings.ToLower(name)] = &Func{Name: name, Min: 0, Max: max, Status: true,
		fn: func([]Value) Value { return Unknown() }}
}

func init() {
	reg("and", 2, -1, fnAnd)
	reg("or", 2, -1, fnOr)
	reg("not", 1, 1, fnNot)
	reg("xor", 2, 2, fnXor)
	reg("eq", 2, 2, fnEq)
	reg("ne", 2, 2, fnNe)
	reg("gt", 2, 2, cmpFn(func(c int) bool { return c > 0 }))
	reg("ge", 2, 2, cmpFn(func(c int) bool { return c >= 0 }))
	reg("lt", 2, 2, cmpFn(func(c int) bool { return c < 0 }))
	reg("le", 2, 2, cmpFn(func(c int) bool { return c <= 0 }))
	reg("contains", 2, 2, strFn2(func(a, b string) bool { return strings.Contains(a, b) }))
	reg("startsWith", 2, 2, strFn2(strings.HasPrefix))
	reg("endsWith", 2, 2, strFn2(strings.HasSuffix))
	reg("containsValue", 2, 2, fnContainsValue)
	reg("in", 1, -1, fnIn)
	reg("notIn", 1, -1, fnNotIn)
	reg("coalesce", 2, -1, fnCoalesce)
	reg("convertToJson", 1, 1, fnConvertToJSON)
	reg("counter", 2, 2, func([]Value) Value { return Unknown() })
	reg("format", 1, -1, fnFormat)
	reg("iif", 1, 3, fnIif)
	reg("join", 2, 2, fnJoin)
	reg("length", 1, 1, fnLength)
	reg("lower", 1, 1, strFn1(strings.ToLower))
	reg("upper", 1, 1, strFn1(strings.ToUpper))
	reg("trim", 1, 1, strFn1(strings.TrimSpace))
	reg("replace", 3, 3, fnReplace)
	reg("split", 2, 2, fnSplit)
	regStatus("always", 0)
	regStatus("canceled", 0)
	regStatus("failed", -1)
	regStatus("succeeded", -1)
	regStatus("succeededOrFailed", -1)
}

// Funcs returns all built-in functions sorted by name.
func Funcs() []*Func {
	out := make([]*Func, 0, len(funcs))
	for _, f := range funcs {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// Arity describes how many arguments the function takes.
func (f *Func) Arity() string { return arity(f) }

// LookupFunc returns the built-in function with the given (case-insensitive) name.
func LookupFunc(name string) (*Func, bool) {
	f, ok := funcs[strings.ToLower(name)]
	return f, ok
}

func call(c *Call, ctx *Context) Value {
	f, ok := LookupFunc(c.Name)
	if !ok || len(c.Args) < f.Min || (f.Max >= 0 && len(c.Args) > f.Max) {
		return Unknown()
	}
	if f.Status && len(c.Args) == 0 && ctx != nil {
		if v, ok := ctx.Status[strings.ToLower(c.Name)]; ok {
			return v
		}
	}
	args := make([]Value, len(c.Args))
	for i, a := range c.Args {
		args[i] = Eval(a, ctx)
	}
	return f.fn(args)
}

// CheckCalls reports calls to unknown functions and calls with the wrong number of arguments.
func CheckCalls(n Node) []*SyntaxError {
	var errs []*SyntaxError
	Walk(n, func(x Node) bool {
		c, ok := x.(*Call)
		if !ok {
			return true
		}
		f, ok := LookupFunc(c.Name)
		if !ok {
			errs = append(errs, &SyntaxError{Pos: c.Pos, Msg: fmt.Sprintf("unknown function %q", c.Name)})
			return true
		}
		if len(c.Args) < f.Min || (f.Max >= 0 && len(c.Args) > f.Max) {
			errs = append(errs, &SyntaxError{Pos: c.Pos, Msg: fmt.Sprintf("%s() takes %s, got %d", f.Name, arity(f), len(c.Args))})
		}
		return true
	})
	return errs
}

func arity(f *Func) string {
	switch {
	case f.Max < 0:
		return fmt.Sprintf("at least %d argument(s)", f.Min)
	case f.Min == f.Max:
		return fmt.Sprintf("exactly %d argument(s)", f.Min)
	}
	return fmt.Sprintf("%d to %d arguments", f.Min, f.Max)
}

func fnAnd(args []Value) Value {
	unknown := false
	for _, a := range args {
		b, ok := a.Truthy()
		if !ok {
			unknown = true
			continue
		}
		if !b {
			return Bool(false)
		}
	}
	if unknown {
		return Unknown()
	}
	return Bool(true)
}

func fnOr(args []Value) Value {
	unknown := false
	for _, a := range args {
		b, ok := a.Truthy()
		if !ok {
			unknown = true
			continue
		}
		if b {
			return Bool(true)
		}
	}
	if unknown {
		return Unknown()
	}
	return Bool(false)
}

func fnNot(args []Value) Value {
	b, ok := args[0].Truthy()
	if !ok {
		return Unknown()
	}
	return Bool(!b)
}

func fnXor(args []Value) Value {
	a, ok1 := args[0].Truthy()
	b, ok2 := args[1].Truthy()
	if !ok1 || !ok2 {
		return Unknown()
	}
	return Bool(a != b)
}

func scalarKnown(vs ...Value) bool {
	for _, v := range vs {
		if !v.Known() || v.Kind == KArray || v.Kind == KObject {
			return false
		}
	}
	return true
}

func equal(a, b Value) (eq, ok bool) {
	if !scalarKnown(a, b) {
		return false, false
	}
	c, convOK := compare(a, b)
	return convOK && c == 0, true
}

func fnEq(args []Value) Value {
	eq, ok := equal(args[0], args[1])
	if !ok {
		return Unknown()
	}
	return Bool(eq)
}

func fnNe(args []Value) Value {
	eq, ok := equal(args[0], args[1])
	if !ok {
		return Unknown()
	}
	return Bool(!eq)
}

func cmpFn(pred func(int) bool) func([]Value) Value {
	return func(args []Value) Value {
		if !scalarKnown(args[0], args[1]) {
			return Unknown()
		}
		c, ok := compare(args[0], args[1])
		if !ok {
			return Unknown()
		}
		return Bool(pred(c))
	}
}

func strFn2(pred func(a, b string) bool) func([]Value) Value {
	return func(args []Value) Value {
		if !scalarKnown(args[0], args[1]) {
			return Unknown()
		}
		return Bool(pred(strings.ToUpper(args[0].String()), strings.ToUpper(args[1].String())))
	}
}

func strFn1(fn func(string) string) func([]Value) Value {
	return func(args []Value) Value {
		if !scalarKnown(args[0]) {
			return Unknown()
		}
		return Str(fn(args[0].String()))
	}
}

func fnContainsValue(args []Value) Value {
	coll, needle := args[0], args[1]
	if !coll.Known() || !scalarKnown(needle) {
		return Unknown()
	}
	var items []Value
	switch coll.Kind {
	case KArray:
		items = coll.Arr
	case KObject:
		for _, kv := range coll.Obj {
			items = append(items, kv.Val)
		}
	default:
		return Bool(false)
	}
	unknown := false
	for _, it := range items {
		eq, ok := equal(needle, it)
		if !ok {
			unknown = true
			continue
		}
		if eq {
			return Bool(true)
		}
	}
	if unknown {
		return Unknown()
	}
	return Bool(false)
}

func fnIn(args []Value) Value {
	unknown := false
	for _, a := range args[1:] {
		eq, ok := equal(args[0], a)
		if !ok {
			unknown = true
			continue
		}
		if eq {
			return Bool(true)
		}
	}
	if unknown {
		return Unknown()
	}
	return Bool(false)
}

func fnNotIn(args []Value) Value {
	v := fnIn(args)
	if !v.Known() {
		return v
	}
	return Bool(!v.B)
}

func fnCoalesce(args []Value) Value {
	for _, a := range args {
		switch {
		case !a.Known():
			return Unknown()
		case a.Kind == KNull, a.Kind == KString && a.S == "":
			continue
		default:
			return a
		}
	}
	return Str("")
}

func fnConvertToJSON(args []Value) Value {
	s, ok := args[0].JSON()
	if !ok {
		return Unknown()
	}
	return Str(s)
}

func fnFormat(args []Value) Value {
	if !scalarKnown(args...) {
		return Unknown()
	}
	f := args[0].String()
	var b strings.Builder
	for i := 0; i < len(f); i++ {
		c := f[i]
		if (c == '{' || c == '}') && i+1 < len(f) && f[i+1] == c {
			b.WriteByte(c)
			i++
			continue
		}
		if c != '{' {
			b.WriteByte(c)
			continue
		}
		end := strings.IndexByte(f[i:], '}')
		if end < 0 {
			return Unknown()
		}
		n, err := strconv.Atoi(f[i+1 : i+end])
		if err != nil || n+1 >= len(args) {
			return Unknown()
		}
		b.WriteString(args[n+1].String())
		i += end
	}
	return Str(b.String())
}

func fnIif(args []Value) Value {
	c, ok := args[0].Truthy()
	switch {
	case !ok:
		return Unknown()
	case c && len(args) > 1:
		return args[1]
	case !c && len(args) > 2:
		return args[2]
	}
	return Null()
}

func fnJoin(args []Value) Value {
	sep, coll := args[0], args[1]
	if !scalarKnown(sep) || !coll.Known() {
		return Unknown()
	}
	if coll.Kind != KArray {
		return Str(coll.String())
	}
	parts := make([]string, 0, len(coll.Arr))
	for _, it := range coll.Arr {
		if !it.Known() {
			return Unknown()
		}
		parts = append(parts, it.String())
	}
	return Str(strings.Join(parts, sep.String()))
}

func fnLength(args []Value) Value {
	v := args[0]
	switch v.Kind {
	case KUnknown:
		return Unknown()
	case KArray:
		return Num(float64(len(v.Arr)))
	case KObject:
		return Num(float64(len(v.Obj)))
	}
	return Num(float64(len(v.String())))
}

func fnReplace(args []Value) Value {
	if !scalarKnown(args...) {
		return Unknown()
	}
	return Str(strings.ReplaceAll(args[0].String(), args[1].String(), args[2].String()))
}

func fnSplit(args []Value) Value {
	if !scalarKnown(args...) {
		return Unknown()
	}
	s, delims := args[0].String(), args[1].String()
	out := Value{Kind: KArray}
	if delims == "" {
		out.Arr = []Value{Str(s)}
		return out
	}
	start := 0
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(delims, s[i]) >= 0 {
			out.Arr = append(out.Arr, Str(s[start:i]))
			start = i + 1
		}
	}
	out.Arr = append(out.Arr, Str(s[start:]))
	return out
}
