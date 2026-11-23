// Package yamlx loads YAML sources with position information and provides node helpers.
package yamlx

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/danielsada/go-ado-pipelines-lint/internal/diag"
)

// Rule IDs emitted by this package.
const (
	RuleSyntax       = "ADO001"
	RuleDuplicateKey = "ADO002"
)

// Source is a parsed YAML file.
type Source struct {
	// Path is the display path used in diagnostics.
	Path string
	// Abs is the absolute filesystem path ("" for in-memory sources).
	Abs   string
	Data  []byte
	Lines []string
	// Root is the document's top-level node, or nil if the file is empty or invalid.
	Root *yaml.Node
	// Invalid is true if the file failed to parse.
	Invalid bool
	// ParseDiags holds syntax and duplicate-key diagnostics found while parsing.
	ParseDiags []diag.Diagnostic
}

var lineRe = regexp.MustCompile(`line (\d+)`)

// Parse parses data as YAML. Syntax errors and duplicate keys are returned as diagnostics.
func Parse(path, abs string, data []byte) (*Source, []diag.Diagnostic) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	src := &Source{Path: path, Abs: abs, Data: []byte(text), Lines: strings.Split(text, "\n")}
	var doc yaml.Node
	if err := yaml.Unmarshal(src.Data, &doc); err != nil {
		src.Invalid = true
		src.ParseDiags = []diag.Diagnostic{syntaxDiag(path, err)}
		return src, src.ParseDiags
	}
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		src.Root = doc.Content[0]
	}
	var ds []diag.Diagnostic
	if src.Root != nil {
		ds = duplicateKeys(src, src.Root, ds)
	}
	src.ParseDiags = ds
	return src, ds
}

func syntaxDiag(path string, err error) diag.Diagnostic {
	msg := strings.TrimPrefix(err.Error(), "yaml: ")
	line := 1
	if m := lineRe.FindStringSubmatch(msg); m != nil {
		if n, convErr := strconv.Atoi(m[1]); convErr == nil {
			line = n
		}
		msg = strings.TrimSpace(strings.Replace(msg, m[0]+":", "", 1))
	}
	return diag.Diagnostic{
		RuleID:   RuleSyntax,
		Severity: diag.Error,
		Message:  "invalid YAML: " + msg,
		Location: diag.Location{File: path, Line: line, Column: 1},
	}
}

func duplicateKeys(src *Source, n *yaml.Node, ds []diag.Diagnostic) []diag.Diagnostic {
	switch n.Kind {
	case yaml.MappingNode:
		seen := map[string]*yaml.Node{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind == yaml.ScalarNode {
				if prev, ok := seen[k.Value]; ok {
					ds = append(ds, diag.Diagnostic{
						RuleID:   RuleDuplicateKey,
						Severity: diag.Error,
						Message:  fmt.Sprintf("duplicate key %q (first defined on line %d)", k.Value, prev.Line),
						Location: diag.Location{File: src.Path, Line: k.Line, Column: k.Column},
					})
				} else {
					seen[k.Value] = k
				}
			}
			ds = duplicateKeys(src, n.Content[i+1], ds)
		}
	case yaml.SequenceNode:
		for _, c := range n.Content {
			ds = duplicateKeys(src, c, ds)
		}
	}
	return ds
}

// Deref follows alias nodes.
func Deref(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	return n
}

// IsMap reports whether n is a mapping node.
func IsMap(n *yaml.Node) bool { n = Deref(n); return n != nil && n.Kind == yaml.MappingNode }

// IsSeq reports whether n is a sequence node.
func IsSeq(n *yaml.Node) bool { n = Deref(n); return n != nil && n.Kind == yaml.SequenceNode }

// IsScalar reports whether n is a scalar node.
func IsScalar(n *yaml.Node) bool { n = Deref(n); return n != nil && n.Kind == yaml.ScalarNode }

// IsNull reports whether n is absent or an explicit YAML null.
func IsNull(n *yaml.Node) bool {
	n = Deref(n)
	return n == nil || (n.Kind == yaml.ScalarNode && n.Tag == "!!null")
}

// Get returns the key and value nodes for key in mapping m (exact match).
func Get(m *yaml.Node, key string) (k, v *yaml.Node) {
	m = Deref(m)
	if m == nil || m.Kind != yaml.MappingNode {
		return nil, nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i], Deref(m.Content[i+1])
		}
	}
	return nil, nil
}

// GetFold is like Get but matches keys case-insensitively.
func GetFold(m *yaml.Node, key string) (k, v *yaml.Node) {
	m = Deref(m)
	if m == nil || m.Kind != yaml.MappingNode {
		return nil, nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if strings.EqualFold(m.Content[i].Value, key) {
			return m.Content[i], Deref(m.Content[i+1])
		}
	}
	return nil, nil
}

// Value returns the value node for key in mapping m.
func Value(m *yaml.Node, key string) *yaml.Node {
	_, v := Get(m, key)
	return v
}

// Str returns the scalar value of key in mapping m, or "".
func Str(m *yaml.Node, key string) string {
	v := Value(m, key)
	if v == nil || v.Kind != yaml.ScalarNode {
		return ""
	}
	return v.Value
}

// Has reports whether mapping m contains key.
func Has(m *yaml.Node, key string) bool {
	k, _ := Get(m, key)
	return k != nil
}

// Pairs calls fn for each key/value pair of mapping m.
func Pairs(m *yaml.Node, fn func(k, v *yaml.Node)) {
	m = Deref(m)
	if m == nil || m.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		fn(m.Content[i], Deref(m.Content[i+1]))
	}
}

// Items returns the items of sequence n (nil if n is not a sequence).
func Items(n *yaml.Node) []*yaml.Node {
	n = Deref(n)
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	out := make([]*yaml.Node, len(n.Content))
	for i, c := range n.Content {
		out[i] = Deref(c)
	}
	return out
}

// StringList returns a string or a sequence of strings as a slice, with the corresponding nodes.
func StringList(n *yaml.Node) ([]string, []*yaml.Node) {
	n = Deref(n)
	if n == nil {
		return nil, nil
	}
	if n.Kind == yaml.ScalarNode {
		if n.Tag == "!!null" {
			return nil, nil
		}
		return []string{n.Value}, []*yaml.Node{n}
	}
	var out []string
	var nodes []*yaml.Node
	for _, c := range Items(n) {
		if c.Kind == yaml.ScalarNode {
			out = append(out, c.Value)
			nodes = append(nodes, c)
		}
	}
	return out, nodes
}

// Scalar creates a new string scalar node positioned at pos.
func Scalar(value string, pos *yaml.Node) *yaml.Node {
	n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
	if pos != nil {
		n.Line, n.Column = pos.Line, pos.Column
	}
	return n
}

// KindName returns a human-readable name for a node's kind.
func KindName(n *yaml.Node) string {
	n = Deref(n)
	switch {
	case n == nil:
		return "nothing"
	case n.Kind == yaml.MappingNode:
		return "a mapping"
	case n.Kind == yaml.SequenceNode:
		return "a sequence"
	case IsNull(n):
		return "null"
	default:
		return "a scalar"
	}
}
