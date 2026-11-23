package rules

import (
	"errors"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/danielsada/go-ado-pipelines-lint/internal/expr"
	"github.com/danielsada/go-ado-pipelines-lint/internal/lint"
	"github.com/danielsada/go-ado-pipelines-lint/internal/template"
)

// occurrence is an expression found in a source file.
type occurrence struct {
	visit scalarVisit
	// off is the byte offset of the expression text within the scalar.
	off  int
	text string
	ast  expr.Node
	err  error
	// directive is set for ${{ if/elseif/each/... }} keys.
	directive *expr.Directive
	condition bool
	runtime   bool
}

func (o occurrence) node() *yaml.Node { return o.visit.Node }

// collectExpressions finds every compile-time, runtime and condition expression in a source.
func collectExpressions(root *yaml.Node) []occurrence {
	var out []occurrence
	walkScalars(root, func(v scalarVisit) {
		s := v.Node.Value
		if v.IsKey && expr.HasTemplate(s) {
			if d, err := expr.ParseDirective(s); d != nil {
				out = append(out, directiveOccurrence(v, d, err))
				return
			}
		}
		if !v.IsKey && v.last() == "condition" && v.Parent != nil && isUnit(v.Parent) && !expr.HasTemplate(s) && strings.TrimSpace(s) != "" {
			ast, err := expr.Parse(s)
			out = append(out, occurrence{visit: v, text: s, ast: ast, err: err, condition: true})
			return
		}
		out = append(out, spanOccurrences(v)...)
	})
	return out
}

func directiveOccurrence(v scalarVisit, d *expr.Directive, err error) occurrence {
	o := occurrence{visit: v, off: d.Offset, text: d.Text, err: err, directive: d}
	switch d.Kind {
	case expr.DirIf, expr.DirElseIf:
		o.ast = d.Cond
	case expr.DirEach:
		o.ast = d.Coll
	case expr.DirExpr:
		o.ast = d.Expr
	}
	if d.Kind == expr.DirIf || d.Kind == expr.DirElseIf {
		o.off = d.Offset + strings.Index(d.Text, " ") + 1
	}
	if d.Kind == expr.DirEach && d.Coll != nil {
		o.off = d.Offset + strings.LastIndex(d.Text, " in ") + 4
	}
	return o
}

func spanOccurrences(v scalarVisit) []occurrence {
	script := isScriptValue(v)
	spans, scanErrs := expr.Scan(v.Node.Value)
	var out []occurrence
	for _, se := range scanErrs {
		if se.Kind == expr.SpanRuntime && script {
			continue
		}
		out = append(out, occurrence{visit: v, off: se.Pos, err: errors.New(se.Msg)})
	}
	for _, sp := range spans {
		if sp.Kind == expr.SpanMacro || (sp.Kind == expr.SpanRuntime && (script || expr.HasTemplate(sp.Expr))) {
			continue
		}
		ast, err := expr.Parse(sp.Expr)
		out = append(out, occurrence{visit: v, off: sp.ExprStart, text: sp.Expr, ast: ast, err: err, runtime: sp.Kind == expr.SpanRuntime})
	}
	return out
}

func errOffset(err error) int {
	var se *expr.SyntaxError
	if errors.As(err, &se) {
		return se.Pos
	}
	return 0
}

func errMessage(err error) string {
	var se *expr.SyntaxError
	if errors.As(err, &se) {
		return se.Msg
	}
	return err.Error()
}

func checkExpressionSyntax(c *lint.SourceContext) {
	for _, o := range collectExpressions(c.Src.Root) {
		if o.err == nil {
			continue
		}
		what := "expression"
		switch {
		case o.directive != nil:
			what = "template directive"
		case o.condition:
			what = "condition"
		}
		off := o.off
		if o.text != "" {
			off += errOffset(o.err)
		}
		c.ReportAt("ADO301", at(c, o.node(), off), fmt.Sprintf("invalid %s: %s", what, errMessage(o.err)))
	}
}

func checkFunctions(c *lint.SourceContext) {
	for _, o := range collectExpressions(c.Src.Root) {
		if o.ast == nil {
			continue
		}
		for _, e := range expr.CheckCalls(o.ast) {
			c.ReportAt("ADO302", at(c, o.node(), o.off+e.Pos), e.Msg)
		}
	}
}

func checkRuntimePartial(c *lint.SourceContext) {
	walkScalars(c.Src.Root, func(v scalarVisit) {
		if v.IsKey || !v.under("variables") || isScriptValue(v) || !strings.Contains(v.Node.Value, "$[") {
			return
		}
		switch v.last() {
		case "name", "group", "template", "readonly":
			return
		}
		if _, ok := expr.Whole(v.Node.Value, expr.SpanRuntime); ok {
			return
		}
		c.Report("ADO303", v.Node, "a runtime expression $[ ] must be the entire value of a variable; it is not expanded when combined with other text")
	})
}

func checkTemplateLocation(c *lint.SourceContext) {
	walkScalars(c.Src.Root, func(v scalarVisit) {
		if !expr.HasTemplate(v.Node.Value) || len(v.Path) == 0 {
			return
		}
		top := v.Path[0]
		switch {
		case top == "trigger" || top == "pr" || top == "schedules":
		case top == "resources" && len(v.Path) > 1 && v.Path[1] != "containers" && v.last() != "ref":
		default:
			return
		}
		c.Report("ADO304", v.Node, fmt.Sprintf("template expressions are not expanded in %s; only stages, jobs, steps and resources.containers support ${{ }}", strings.Join(v.Path[:min(2, len(v.Path))], ".")))
	})
}

// paramUse records references to parameters in a source.
type paramUse struct {
	names map[string]bool
	whole bool
}

func parameterRefs(occ []occurrence, fn func(name string, o occurrence, off int)) paramUse {
	use := paramUse{names: map[string]bool{}}
	for _, o := range occ {
		if o.ast == nil || o.runtime || o.condition {
			continue
		}
		idents, members := 0, 0
		expr.Walk(o.ast, func(x expr.Node) bool {
			if id, ok := x.(*expr.Ident); ok && strings.EqualFold(id.Name, "parameters") {
				idents++
				return true
			}
			name, base, ok := memberOf(x)
			if ok && strings.EqualFold(base.Name, "parameters") {
				members++
				use.names[strings.ToLower(name)] = true
				if fn != nil {
					fn(name, o, o.off+base.Pos)
				}
			}
			return true
		})
		if idents > members {
			use.whole = true
		}
	}
	return use
}

// memberOf matches X.name or X['name'] where X is an identifier.
func memberOf(x expr.Node) (string, *expr.Ident, bool) {
	switch t := x.(type) {
	case *expr.Member:
		if id, ok := t.X.(*expr.Ident); ok {
			return t.Name, id, true
		}
	case *expr.Index:
		id, ok := t.X.(*expr.Ident)
		lit, isLit := t.Index.(*expr.Literal)
		if ok && isLit && lit.Val.Kind == expr.KString {
			return lit.Val.S, id, true
		}
	}
	return "", nil, false
}

func checkUndeclaredParams(c *lint.SourceContext) {
	if c.IsTemplate && !template.StrictParams(c.Params) {
		return // legacy or missing declarations: callers may pass any parameter
	}
	parameterRefs(collectExpressions(c.Src.Root), func(name string, o occurrence, off int) {
		if template.FindParam(c.Params, name) == nil {
			c.ReportAt("ADO206", at(c, o.node(), off), fmt.Sprintf("parameters.%s is not declared in this file's parameters", name))
		}
	})
}

func checkUnusedParams(c *lint.SourceContext) {
	use := parameterRefs(collectExpressions(c.Src.Root), nil)
	if use.whole {
		return
	}
	for _, p := range c.Params {
		if !use.names[strings.ToLower(p.Name)] {
			c.Report("ADO207", p.NameNode, fmt.Sprintf("parameter %q is declared but never used", p.Name))
		}
	}
}
