package rules

import (
	"fmt"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/danielsada/go-ado-pipelines-lint/internal/expr"
	"github.com/danielsada/go-ado-pipelines-lint/internal/lint"
	"github.com/danielsada/go-ado-pipelines-lint/internal/model"
	"github.com/danielsada/go-ado-pipelines-lint/internal/template"
	"github.com/danielsada/go-ado-pipelines-lint/internal/yamlx"
)

// unitConditions calls fn for each stage/job/step condition that is a plain expression.
func unitConditions(root *yaml.Node, fn func(unit, cond *yaml.Node, ast expr.Node)) {
	walkMappings(root, nil, func(m *yaml.Node, path []string) {
		if !isUnit(m) || (len(path) > 0 && path[len(path)-1] == "parameters") {
			return
		}
		cv, ast := parsedCondition(m)
		if ast != nil {
			fn(m, cv, ast)
		}
	})
}

func checkConstantCondition(c *lint.SourceContext) {
	unitConditions(c.Src.Root, func(unit, cond *yaml.Node, ast expr.Node) {
		b, ok := expr.Eval(ast, expr.NewContext()).Truthy()
		if !ok {
			return
		}
		kind := unitKind(unit)
		if b {
			c.Report("ADO601", cond, fmt.Sprintf("condition is always true; the %s also runs after failures and when the run is canceled (a custom condition replaces the default succeeded())", kind))
			return
		}
		hint := "remove the " + kind
		if kind == "step" {
			hint = "use enabled: false or remove the step"
		}
		c.Report("ADO601", cond, fmt.Sprintf("condition is always false, so the %s never runs; %s", kind, hint))
	})
}

// statusAware reports whether a condition checks job/stage status.
func statusAware(ast expr.Node) bool {
	aware := false
	expr.Walk(ast, func(x expr.Node) bool {
		switch t := x.(type) {
		case *expr.Call:
			if f, ok := expr.LookupFunc(t.Name); ok && f.Status {
				aware = true
			}
		case *expr.Member:
			if strings.EqualFold(t.Name, "result") {
				aware = true
			}
		case *expr.Literal:
			if t.Val.Kind == expr.KString && strings.EqualFold(t.Val.S, "Agent.JobStatus") {
				aware = true
			}
		}
		return !aware
	})
	return aware
}

func checkConditionOverridesDefault(c *lint.SourceContext) {
	unitConditions(c.Src.Root, func(unit, cond *yaml.Node, ast expr.Node) {
		if _, constant := expr.Eval(ast, expr.NewContext()).Truthy(); constant || statusAware(ast) || isPublishStep(unit) || noUpstream(unit) {
			return
		}
		c.Report("ADO603", cond, fmt.Sprintf("condition does not check status, so the %s runs even if earlier work failed (a custom condition replaces the default succeeded()); use and(succeeded(), %s)", unitKind(unit), strings.TrimSpace(cond.Value)))
	})
}

func checkAlwaysRuns(c *lint.SourceContext) {
	unitConditions(c.Src.Root, func(unit, cond *yaml.Node, ast expr.Node) {
		if isPublishStep(unit) {
			return
		}
		found := false
		expr.Walk(ast, func(x expr.Node) bool {
			if call, ok := x.(*expr.Call); ok && strings.EqualFold(call.Name, "always") {
				found = true
			}
			return !found
		})
		if found {
			c.Report("ADO604", cond, fmt.Sprintf("always() also runs the %s when the run is canceled; use succeededOrFailed() or not(canceled()) unless that is intended", unitKind(unit)))
		}
	})
}

// isPublishStep reports whether a step publishes results, logs or artifacts. Such steps are meant to
// run after failures (and often on cancellation), so conditions without succeeded() or with always()
// are intentional there.
func isPublishStep(m *yaml.Node) bool {
	if yamlx.Has(m, "publish") || yamlx.Has(m, "upload") {
		return true
	}
	task := strings.ToLower(yamlx.Str(m, "task"))
	return strings.HasPrefix(task, "publish") || strings.HasPrefix(task, "copyfiles@")
}

// noUpstream reports whether a job or stage has nothing it depends on, so the default succeeded()
// condition is always true and a custom condition can't drop it: a job without dependsOn, or a
// stage or job with an explicitly empty dependsOn.
func noUpstream(m *yaml.Node) bool {
	k, deps := yamlx.Get(m, "dependsOn")
	if k != nil {
		return yamlx.IsSeq(deps) && len(deps.Content) == 0
	}
	return yamlx.Has(m, "job") || yamlx.Has(m, "deployment")
}

// maxCombos bounds the parameter value combinations explored for template conditions.
const maxCombos = 256

// paramDomain returns the finite set of values a parameter can take, if known.
func paramDomain(p *template.Param) []expr.Value {
	if p == nil || !p.Typed {
		return nil
	}
	if strings.EqualFold(p.Type, "boolean") {
		return []expr.Value{expr.Bool(true), expr.Bool(false)}
	}
	var out []expr.Value
	for _, v := range p.Values {
		if strings.EqualFold(p.Type, "number") {
			if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				out = append(out, expr.Num(f))
			}
			continue
		}
		out = append(out, expr.Str(v))
	}
	return out
}

// constantCondition evaluates a template condition for every combination of allowed parameter
// values. It returns the constant result and the parameters involved, or ok=false if the result varies.
func constantCondition(ast expr.Node, params []*template.Param) (value bool, involved []string, ok bool) {
	var names []string
	var domains [][]expr.Value
	combos := 1
	undeclared := false
	parameterRefs([]occurrence{{ast: ast}}, func(name string, _ occurrence, _ int) {
		p := template.FindParam(params, name)
		if p == nil {
			undeclared = true
			return
		}
		d := paramDomain(p)
		if len(d) == 0 || containsFold(names, p.Name) {
			return
		}
		names, domains = append(names, p.Name), append(domains, d)
		combos *= len(d)
	})
	if undeclared || combos > maxCombos {
		return false, nil, false
	}
	first, have := false, false
	for i := 0; i < combos; i++ {
		ctx := expr.NewContext()
		ctx.Set("parameters", comboParams(params, names, domains, i))
		b, known := expr.Eval(ast, ctx).Truthy()
		if !known || (have && b != first) {
			return false, nil, false
		}
		first, have = b, true
	}
	return first, names, true
}

func comboParams(params []*template.Param, names []string, domains [][]expr.Value, i int) expr.Value {
	obj := expr.Value{Kind: expr.KObject}
	for _, p := range params {
		v := expr.Unknown()
		idx := i
		for j, n := range names {
			if strings.EqualFold(n, p.Name) {
				v = domains[j][idx%len(domains[j])]
			}
			idx /= len(domains[j])
		}
		obj.Obj = append(obj.Obj, expr.KV{Key: p.Name, Val: v})
	}
	return obj
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

func checkConstantTemplateIf(c *lint.SourceContext) {
	walkMappingsAndSeqs(c.Src.Root, func(keys []*yaml.Node) {
		for i, k := range keys {
			d, err := expr.ParseDirective(k.Value)
			if d == nil || err != nil || (d.Kind != expr.DirIf && d.Kind != expr.DirElseIf) {
				continue
			}
			b, names, ok := constantCondition(d.Cond, c.Params)
			if !ok {
				continue
			}
			c.Report("ADO602", k, templateIfMessage(d, b, names, followedByBranch(keys[i+1:])))
		}
	})
}

func templateIfMessage(d *expr.Directive, b bool, names []string, more bool) string {
	word := "if"
	if d.Kind == expr.DirElseIf {
		word = "elseif"
	}
	msg := fmt.Sprintf("${{ %s }} condition is always %t", word, b)
	if len(names) > 0 {
		msg += " for every allowed value of parameters." + strings.Join(names, ", parameters.")
	}
	if b && more {
		return msg + "; the following elseif/else branches are unreachable"
	}
	if !b {
		return msg + "; its branch is never included"
	}
	return msg
}

func followedByBranch(rest []*yaml.Node) bool {
	if len(rest) == 0 {
		return false
	}
	d, _ := expr.ParseDirective(rest[0].Value)
	return d != nil && (d.Kind == expr.DirElseIf || d.Kind == expr.DirElse)
}

// walkMappingsAndSeqs calls fn with the ordered directive-capable keys of each mapping, and
// with the single keys of consecutive sequence items (where if/elseif/else chains also live).
func walkMappingsAndSeqs(n *yaml.Node, fn func(keys []*yaml.Node)) {
	n = yamlx.Deref(n)
	if n == nil {
		return
	}
	switch n.Kind {
	case yaml.MappingNode:
		var keys []*yaml.Node
		for i := 0; i+1 < len(n.Content); i += 2 {
			keys = append(keys, n.Content[i])
			walkMappingsAndSeqs(n.Content[i+1], fn)
		}
		fn(keys)
	case yaml.SequenceNode:
		var keys []*yaml.Node
		for _, it := range n.Content {
			it = yamlx.Deref(it)
			if yamlx.IsMap(it) && len(it.Content) == 2 && expr.IsDirective(it.Content[0].Value) {
				keys = append(keys, it.Content[0])
				walkMappingsAndSeqs(it.Content[1], fn)
				continue
			}
			if len(keys) > 0 {
				fn(keys)
				keys = nil
			}
			walkMappingsAndSeqs(it, fn)
		}
		fn(keys)
	}
}

type rtExpr struct {
	node *yaml.Node
	ast  expr.Node
}

// runtimeExprs returns the parsed condition and $[ ] variable expressions of a stage or job.
func runtimeExprs(cond *yaml.Node, vars []*model.Variable) []rtExpr {
	var out []rtExpr
	if yamlx.IsScalar(cond) && !expr.HasTemplate(cond.Value) {
		if ast, err := expr.Parse(cond.Value); err == nil {
			out = append(out, rtExpr{cond, ast})
		}
	}
	for _, v := range vars {
		if !yamlx.IsScalar(v.Value) {
			continue
		}
		if sp, ok := expr.Whole(v.Value.Value, expr.SpanRuntime); ok {
			if ast, err := expr.Parse(sp.Expr); err == nil {
				out = append(out, rtExpr{v.Value, ast})
			}
		}
	}
	return out
}

func nameSet(refs []model.Ref) map[string]bool {
	m := map[string]bool{}
	for _, r := range refs {
		m[strings.ToLower(r.Name)] = true
	}
	return m
}

// stageDeps returns the stages a stage depends on (explicit dependsOn, else the previous stage).
func stageDeps(s *model.Stage) map[string]bool {
	if s.HasDependsOn {
		return nameSet(s.DependsOn)
	}
	m := map[string]bool{}
	if s.Index > 0 && s.Pipeline != nil && s.Index-1 < len(s.Pipeline.Stages) {
		m[strings.ToLower(s.Pipeline.Stages[s.Index-1].Name)] = true
	}
	return m
}

func checkDependencyDeclared(c *lint.PipelineContext) {
	if c.Pipeline.Partial {
		return // dependsOn lists and dependencies may come from the including pipeline
	}
	for _, s := range c.Pipeline.Stages {
		if !s.Implicit {
			sd := stageDeps(s)
			for _, re := range runtimeExprs(s.Condition, s.Variables) {
				deps, sdeps := dependencyRefs(re.ast)
				reportMissingDeps(c, re.node, append(deps, sdeps...), sd, "stage", s.Name)
			}
		}
		for _, j := range s.Jobs {
			if j.Implicit {
				continue
			}
			jd := nameSet(j.DependsOn)
			for _, re := range runtimeExprs(j.Condition, j.Variables) {
				deps, sdeps := dependencyRefs(re.ast)
				reportMissingDeps(c, re.node, deps, jd, "job", j.Name)
				if !s.Implicit {
					reportMissingDeps(c, re.node, sdeps, stageDeps(s), "stage", s.Name)
				}
			}
		}
	}
}

func reportMissingDeps(c *lint.PipelineContext, at *yaml.Node, refs []string, declared map[string]bool, kind, name string) {
	seen := map[string]bool{}
	for _, r := range refs {
		lr := strings.ToLower(r)
		if declared[lr] || seen[lr] || hasDynamic(r) || hasDynamic(name) || c.Doc.Conditional(at) {
			continue
		}
		seen[lr] = true
		c.Report("ADO605", at, fmt.Sprintf("references dependencies of %q, but %s %q does not depend on it; add %s to dependsOn", r, kind, name, r))
	}
}
