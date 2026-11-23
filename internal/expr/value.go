package expr

import (
	"encoding/json"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Kind is the type of a Value.
type Kind int

// Value kinds. KUnknown represents a value that cannot be determined statically.
const (
	KUnknown Kind = iota
	KNull
	KBool
	KNumber
	KString
	KVersion
	KArray
	KObject
)

// KV is an object property.
type KV struct {
	Key string
	Val Value
}

// Value is an expression value.
type Value struct {
	Kind Kind
	B    bool
	N    float64
	S    string
	Arr  []Value
	Obj  []KV
	// Node is the YAML node the value was read from, if any. It lets template
	// insertion splice the original nodes (and their positions) back into the tree.
	Node *yaml.Node
}

// Unknown returns the unknown value.
func Unknown() Value { return Value{Kind: KUnknown} }

// Null returns the null value.
func Null() Value { return Value{Kind: KNull} }

// Bool returns a boolean value.
func Bool(b bool) Value { return Value{Kind: KBool, B: b} }

// Num returns a number value.
func Num(f float64) Value { return Value{Kind: KNumber, N: f} }

// Str returns a string value.
func Str(s string) Value { return Value{Kind: KString, S: s} }

// Version returns a version value.
func Version(s string) Value { return Value{Kind: KVersion, S: s} }

// Known reports whether the value is not unknown.
func (v Value) Known() bool { return v.Kind != KUnknown }

// Get returns an object property (case-insensitive). ok is false if missing.
func (v Value) Get(key string) (Value, bool) {
	for _, kv := range v.Obj {
		if strings.EqualFold(kv.Key, key) {
			return kv.Val, true
		}
	}
	return Value{}, false
}

// FromNode converts a YAML node to a Value. Scalars become strings (or null).
func FromNode(n *yaml.Node) Value {
	for n != nil && n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	if n == nil {
		return Null()
	}
	switch n.Kind {
	case yaml.ScalarNode:
		return scalarValue(n)
	case yaml.SequenceNode:
		v := Value{Kind: KArray, Node: n}
		for _, c := range n.Content {
			v.Arr = append(v.Arr, FromNode(c))
		}
		return v
	case yaml.MappingNode:
		v := Value{Kind: KObject, Node: n}
		for i := 0; i+1 < len(n.Content); i += 2 {
			v.Obj = append(v.Obj, KV{Key: n.Content[i].Value, Val: FromNode(n.Content[i+1])})
		}
		return v
	}
	return Null()
}

// scalarValue converts a scalar node. Plain (unquoted) YAML booleans, numbers and nulls keep their
// types, as in Azure Pipelines' YAML reader; quoted scalars are always strings.
func scalarValue(n *yaml.Node) Value {
	if n.Style&(yaml.SingleQuotedStyle|yaml.DoubleQuotedStyle|yaml.LiteralStyle|yaml.FoldedStyle) == 0 {
		switch n.Tag {
		case "!!null":
			return Value{Kind: KNull, Node: n}
		case "!!bool":
			return Value{Kind: KBool, B: strings.EqualFold(n.Value, "true"), Node: n}
		case "!!int", "!!float":
			if f, err := strconv.ParseFloat(strings.ReplaceAll(n.Value, "_", ""), 64); err == nil {
				return Value{Kind: KNumber, N: f, Node: n}
			}
		}
	}
	return Value{Kind: KString, S: n.Value, Node: n}
}

// Truthy converts the value to a boolean. ok is false for unknown values.
func (v Value) Truthy() (b, ok bool) {
	switch v.Kind {
	case KUnknown:
		return false, false
	case KNull:
		return false, true
	case KBool:
		return v.B, true
	case KNumber:
		return v.N != 0, true
	case KString:
		return v.S != "", true
	}
	return true, true
}

// String converts the value to its string form (as used in string interpolation).
func (v Value) String() string {
	switch v.Kind {
	case KBool:
		if v.B {
			return "True"
		}
		return "False"
	case KNumber:
		return strconv.FormatFloat(v.N, 'f', -1, 64)
	case KString, KVersion:
		return v.S
	case KArray, KObject:
		return ""
	}
	return ""
}

// Literal renders the value as expression source.
func (v Value) Literal() string {
	switch v.Kind {
	case KString:
		return "'" + strings.ReplaceAll(v.S, "'", "''") + "'"
	case KNull:
		return "null"
	case KUnknown:
		return "<unknown>"
	case KArray:
		return "<array>"
	case KObject:
		return "<object>"
	}
	return v.String()
}

// JSON renders the value as indented JSON (used by convertToJson).
func (v Value) JSON() (string, bool) {
	x, ok := v.plain()
	if !ok {
		return "", false
	}
	b, err := json.MarshalIndent(x, "", "  ")
	if err != nil {
		return "", false
	}
	return string(b), true
}

func (v Value) plain() (any, bool) {
	switch v.Kind {
	case KUnknown:
		return nil, false
	case KNull:
		return nil, true
	case KBool:
		return v.B, true
	case KNumber:
		return v.N, true
	case KString, KVersion:
		return v.S, true
	case KArray:
		out := make([]any, 0, len(v.Arr))
		for _, e := range v.Arr {
			p, ok := e.plain()
			if !ok {
				return nil, false
			}
			out = append(out, p)
		}
		return out, true
	}
	out := map[string]any{}
	for _, kv := range v.Obj {
		p, ok := kv.Val.plain()
		if !ok {
			return nil, false
		}
		out[kv.Key] = p
	}
	return out, true
}

// toNumber converts per the documented casting rules.
func (v Value) toNumber() (float64, bool) {
	switch v.Kind {
	case KNull:
		return 0, true
	case KBool:
		if v.B {
			return 1, true
		}
		return 0, true
	case KNumber:
		return v.N, true
	case KString:
		s := strings.ReplaceAll(strings.TrimSpace(v.S), ",", "")
		if s == "" {
			return 0, true
		}
		f, err := strconv.ParseFloat(s, 64)
		return f, err == nil
	}
	return 0, false
}

func parseVersion(s string) ([]int, bool) {
	parts := strings.Split(strings.TrimSpace(s), ".")
	if len(parts) < 2 || len(parts) > 4 {
		return nil, false
	}
	out := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, false
		}
		out[i] = n
	}
	return out, true
}

func (v Value) toVersion() ([]int, bool) {
	switch v.Kind {
	case KVersion, KString:
		return parseVersion(v.S)
	case KNumber:
		return parseVersion(strconv.FormatFloat(v.N, 'f', -1, 64))
	}
	return nil, false
}

// convertTo converts v to the kind of target. ok is false if not convertible.
func (v Value) convertTo(k Kind) (Value, bool) {
	if v.Kind == k {
		return v, true
	}
	switch k {
	case KBool:
		b, ok := v.Truthy()
		return Bool(b), ok
	case KNumber:
		f, ok := v.toNumber()
		return Num(f), ok
	case KString:
		if v.Kind == KArray || v.Kind == KObject {
			return Value{}, false
		}
		return Str(v.String()), true
	case KVersion:
		parts, ok := v.toVersion()
		if !ok {
			return Value{}, false
		}
		ss := make([]string, len(parts))
		for i, p := range parts {
			ss[i] = strconv.Itoa(p)
		}
		return Version(strings.Join(ss, ".")), true
	case KNull:
		if v.Kind == KString && v.S == "" {
			return Null(), true
		}
	}
	return Value{}, false
}

// compare compares a and b after converting b to a's type. ok is false if not comparable.
func compare(a, b Value) (int, bool) {
	cb, ok := b.convertTo(a.Kind)
	if !ok {
		return 0, false
	}
	switch a.Kind {
	case KNull:
		return 0, true
	case KBool:
		return boolInt(a.B) - boolInt(cb.B), true
	case KNumber:
		return cmpFloat(a.N, cb.N), true
	case KString:
		return strings.Compare(strings.ToUpper(a.S), strings.ToUpper(cb.S)), true
	case KVersion:
		va, _ := parseVersion(a.S)
		vb, _ := parseVersion(cb.S)
		return cmpVersion(va, vb), true
	}
	return 0, false
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func cmpVersion(a, b []int) int {
	for i := 0; i < len(a) || i < len(b); i++ {
		x, y := 0, 0
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return cmpFloat(float64(x), float64(y))
		}
	}
	return 0
}
