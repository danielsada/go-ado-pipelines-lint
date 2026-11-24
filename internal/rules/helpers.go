// Package rules contains the built-in lint rules.
package rules

import (
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/danielsada/go-ado-pipelines-lint/internal/diag"
	"github.com/danielsada/go-ado-pipelines-lint/internal/expr"
	"github.com/danielsada/go-ado-pipelines-lint/internal/lint"
	"github.com/danielsada/go-ado-pipelines-lint/internal/yamlx"
)

type metaRule struct{ m lint.Meta }

func (r *metaRule) Meta() *lint.Meta { return &r.m }

type sourceRule struct {
	metaRule
	check func(*lint.SourceContext)
}

func (r *sourceRule) CheckSource(c *lint.SourceContext) { r.check(c) }

type pipelineRule struct {
	metaRule
	check func(*lint.PipelineContext)
}

func (r *pipelineRule) CheckPipeline(c *lint.PipelineContext) { r.check(c) }

func docOnly(m lint.Meta) lint.Rule { return &metaRule{m} }

func source(m lint.Meta, fn func(*lint.SourceContext)) lint.Rule {
	return &sourceRule{metaRule{m}, fn}
}

func pipeline(m lint.Meta, fn func(*lint.PipelineContext)) lint.Rule {
	return &pipelineRule{metaRule{m}, fn}
}

// scalarVisit is a scalar (key or value) found while walking a source tree.
type scalarVisit struct {
	Node *yaml.Node
	// Parent is the mapping that contains the scalar as a key or value (nil for sequence items).
	Parent *yaml.Node
	// Unit is the owning stage/job/step through conditional insertion mappings.
	Unit  *yaml.Node
	IsKey bool
	// Path is the list of mapping keys from the root to the scalar (for keys, including the key itself).
	Path []string
}

// last returns the last path element, or "".
func (v scalarVisit) last() string {
	if len(v.Path) == 0 {
		return ""
	}
	return v.Path[len(v.Path)-1]
}

func (v scalarVisit) under(key string) bool {
	for _, p := range v.Path {
		if p == key {
			return true
		}
	}
	return false
}

func walkScalars(n *yaml.Node, fn func(scalarVisit)) {
	walkScalarsPath(n, nil, nil, nil, fn)
}

func walkScalarsPath(n, parent, unit *yaml.Node, path []string, fn func(scalarVisit)) {
	n = yamlx.Deref(n)
	if n == nil {
		return
	}
	switch n.Kind {
	case yaml.ScalarNode:
		fn(scalarVisit{Node: n, Parent: parent, Unit: unit, Path: path})
	case yaml.SequenceNode:
		for _, c := range n.Content {
			walkScalarsPath(c, nil, nil, path, fn)
		}
	case yaml.MappingNode:
		if isUnit(n) {
			unit = n
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			p := append(append([]string(nil), path...), k.Value)
			fn(scalarVisit{Node: k, Parent: n, Unit: unit, IsKey: true, Path: p})
			var owner *yaml.Node
			if k.Value == "condition" || expr.IsDirective(k.Value) {
				owner = unit
			}
			walkScalarsPath(n.Content[i+1], n, owner, p, fn)
		}
	}
}

// walkMappings calls fn for every mapping in the tree.
func walkMappings(n *yaml.Node, path []string, fn func(m *yaml.Node, path []string)) {
	n = yamlx.Deref(n)
	if n == nil {
		return
	}
	switch n.Kind {
	case yaml.SequenceNode:
		for _, c := range n.Content {
			walkMappings(c, path, fn)
		}
	case yaml.MappingNode:
		fn(n, path)
		for i := 0; i+1 < len(n.Content); i += 2 {
			walkMappings(n.Content[i+1], append(append([]string(nil), path...), n.Content[i].Value), fn)
		}
	}
}

// at returns the location of a byte offset inside scalar n of the context's source.
func at(c *lint.SourceContext, n *yaml.Node, off int) diag.Location {
	line, col := yamlx.OffsetPos(c.Src, n, off)
	return diag.Location{File: c.Src.Path, Line: line, Column: col}
}

// scriptKeys are keys whose values are shell or PowerShell code.
var scriptKeys = map[string]bool{"script": true, "bash": true, "pwsh": true, "powershell": true,
	"inlinescript": true, "inline": true, "arguments": true}

func isScriptValue(v scalarVisit) bool {
	return !v.IsKey && scriptKeys[strings.ToLower(v.last())]
}

func isVariableValue(v scalarVisit) bool {
	for i := len(v.Path) - 1; i >= 0; i-- {
		if v.Path[i] != "variables" {
			continue
		}
		var keys []string
		for _, k := range v.Path[i+1:] {
			if !expr.IsDirective(k) {
				keys = append(keys, k)
			}
		}
		return len(keys) == 1
	}
	return false
}

// unitKeys identify a mapping as a stage, job or step.
var unitKeys = []string{"stage", "job", "deployment", "task", "script", "bash", "pwsh", "powershell",
	"checkout", "download", "downloadBuild", "getPackage", "publish", "upload", "reviewApp"}

func isUnit(m *yaml.Node) bool {
	for _, k := range unitKeys {
		if yamlx.Has(m, k) {
			return true
		}
	}
	return false
}

// unitKind describes a stage/job/step mapping for messages.
func unitKind(m *yaml.Node) string {
	switch {
	case yamlx.Has(m, "stage"):
		return "stage"
	case yamlx.Has(m, "job"), yamlx.Has(m, "deployment"):
		return "job"
	}
	return "step"
}

// parsedCondition returns the parsed condition of a unit mapping, if it is a plain expression.
func parsedCondition(m *yaml.Node) (*yaml.Node, expr.Node) {
	_, cv := yamlx.Get(m, "condition")
	if !yamlx.IsScalar(cv) || yamlx.IsNull(cv) || expr.HasTemplate(cv.Value) || strings.TrimSpace(cv.Value) == "" {
		return nil, nil
	}
	ast, err := expr.Parse(cv.Value)
	if err != nil {
		return cv, nil
	}
	return cv, ast
}

// dependencyRefs returns the names referenced as dependencies.X / stageDependencies.X in an expression.
func dependencyRefs(n expr.Node) (deps, stageDeps []string) {
	expr.Walk(n, func(x expr.Node) bool {
		var base expr.Node
		var name string
		switch t := x.(type) {
		case *expr.Member:
			base, name = t.X, t.Name
		case *expr.Index:
			if lit, ok := t.Index.(*expr.Literal); ok && lit.Val.Kind == expr.KString {
				base, name = t.X, lit.Val.S
			}
		}
		id, ok := base.(*expr.Ident)
		if !ok || name == "*" {
			return true
		}
		switch strings.ToLower(id.Name) {
		case "dependencies":
			deps = append(deps, name)
		case "stagedependencies":
			stageDeps = append(stageDeps, name)
		}
		return true
	})
	return deps, stageDeps
}

func hasDynamic(s string) bool { return expr.HasDynamic(s) }
