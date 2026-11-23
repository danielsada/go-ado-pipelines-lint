// Package template expands Azure Pipelines templates: local template references, extends,
// parameters and compile-time ${{ }} expressions and directives.
package template

import (
	"fmt"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/danielsada/go-ado-pipelines-lint/internal/expr"
	"github.com/danielsada/go-ado-pipelines-lint/internal/yamlx"
)

// Param is a declared template or pipeline parameter.
type Param struct {
	Name     string
	Type     string
	Default  *yaml.Node
	Values   []string
	Node     *yaml.Node
	NameNode *yaml.Node
	// Typed is false for the legacy map form (name: default).
	Typed bool
}

// HasDefault reports whether the parameter declares a default value.
func (p *Param) HasDefault() bool { return p.Default != nil }

// DeclaredParams reads the parameters declared at the top of a pipeline or template file.
func DeclaredParams(root *yaml.Node) []*Param {
	_, ps := yamlx.Get(root, "parameters")
	var out []*Param
	switch {
	case yamlx.IsSeq(ps):
		for _, it := range yamlx.Items(ps) {
			nk, nv := yamlx.Get(it, "name")
			if nk == nil || !yamlx.IsScalar(nv) {
				continue
			}
			p := &Param{Name: nv.Value, Type: "string", Node: it, NameNode: nv, Typed: true}
			if t := yamlx.Str(it, "type"); t != "" {
				p.Type = t
			}
			if dk, dv := yamlx.Get(it, "default"); dk != nil {
				p.Default = dv
			}
			p.Values, _ = yamlx.StringList(yamlx.Value(it, "values"))
			out = append(out, p)
		}
	case yamlx.IsMap(ps):
		yamlx.Pairs(ps, func(k, v *yaml.Node) {
			out = append(out, &Param{Name: k.Value, Type: "object", Default: v, Node: k, NameNode: k})
		})
	}
	return out
}

// FindParam finds a parameter by (case-insensitive) name.
func FindParam(ps []*Param, name string) *Param {
	for _, p := range ps {
		if strings.EqualFold(p.Name, name) {
			return p
		}
	}
	return nil
}

func isScalarType(t string) bool {
	switch strings.ToLower(t) {
	case "string", "number", "boolean":
		return true
	}
	return false
}

// typed converts a parameter value node to a typed expression value and validates it.
// It returns a non-empty problem description if the value does not match the declared type or values.
func typed(p *Param, n *yaml.Node) (expr.Value, string) {
	n = yamlx.Deref(n)
	v := expr.FromNode(n)
	if !p.Typed || n == nil {
		return v, ""
	}
	if n.Kind == yaml.ScalarNode && expr.HasDynamic(n.Value) {
		return expr.Unknown(), ""
	}
	switch strings.ToLower(p.Type) {
	case "boolean":
		if n.Kind != yaml.ScalarNode {
			return v, "must be a boolean, got " + yamlx.KindName(n)
		}
		switch strings.ToLower(n.Value) {
		case "true":
			return withNode(expr.Bool(true), n), ""
		case "false":
			return withNode(expr.Bool(false), n), ""
		}
		return v, fmt.Sprintf("must be a boolean (true or false), got %q", n.Value)
	case "number":
		if n.Kind != yaml.ScalarNode {
			return v, "must be a number, got " + yamlx.KindName(n)
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(n.Value), 64)
		if err != nil {
			return v, fmt.Sprintf("must be a number, got %q", n.Value)
		}
		return withNode(expr.Num(f), n), checkValues(p, n.Value)
	case "string":
		if n.Kind != yaml.ScalarNode {
			return v, "must be a string, got " + yamlx.KindName(n)
		}
		return withNode(expr.Str(n.Value), n), checkValues(p, n.Value)
	case "steplist", "joblist", "stagelist", "deploymentlist", "containerlist", "stringlist":
		if n.Kind != yaml.SequenceNode && !yamlx.IsNull(n) {
			return v, fmt.Sprintf("must be a %s (a sequence), got %s", p.Type, yamlx.KindName(n))
		}
	case "step", "job", "stage", "deployment":
		if n.Kind != yaml.MappingNode {
			return v, fmt.Sprintf("must be a %s (a mapping), got %s", p.Type, yamlx.KindName(n))
		}
	}
	return v, ""
}

func withNode(v expr.Value, n *yaml.Node) expr.Value {
	v.Node = n
	return v
}

func checkValues(p *Param, s string) string {
	if len(p.Values) == 0 {
		return ""
	}
	for _, a := range p.Values {
		if strings.EqualFold(a, s) {
			return ""
		}
	}
	return fmt.Sprintf("must be one of %s, got %q", strings.Join(p.Values, ", "), s)
}

// paramsObject builds the "parameters" context object.
func paramsObject(vals []expr.KV) expr.Value {
	return expr.Value{Kind: expr.KObject, Obj: vals}
}
