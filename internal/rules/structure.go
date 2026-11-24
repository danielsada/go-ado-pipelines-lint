package rules

import (
	"fmt"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/danielsada/go-ado-pipelines-lint/internal/expr"
	"github.com/danielsada/go-ado-pipelines-lint/internal/lint"
	"github.com/danielsada/go-ado-pipelines-lint/internal/model"
	"github.com/danielsada/go-ado-pipelines-lint/internal/yamlx"
)

var rootForms = []string{"stages", "jobs", "steps", "extends", "phases"}

func checkRoot(c *lint.PipelineContext) {
	if c.Pipeline.IsTemplate || !yamlx.IsMap(c.Doc.Root) {
		return
	}
	var found []string
	var second *yaml.Node
	yamlx.Pairs(c.Doc.Root, func(k, _ *yaml.Node) {
		for _, f := range rootForms {
			if k.Value == f {
				found = append(found, f)
				if len(found) == 2 {
					second = k
				}
			}
		}
	})
	if len(found) > 1 {
		c.Report("ADO004", second, fmt.Sprintf("a pipeline can define only one of stages, jobs, steps or extends at the root; found %s", strings.Join(found, " and ")))
	}
}

var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func checkIdentifiers(c *lint.PipelineContext) {
	for _, s := range c.Pipeline.Stages {
		if !s.Implicit {
			checkIdent(c, "stage", s.Name, s.NameNode)
		}
		for _, j := range s.Jobs {
			if j.Implicit {
				continue
			}
			kind := "job"
			if j.Deployment {
				kind = "deployment"
				if strings.EqualFold(j.Name, "deploy") {
					c.Report("ADO101", j.NameNode, `"deploy" is a keyword and cannot be used as a deployment job name`)
					continue
				}
			}
			checkIdent(c, kind, j.Name, j.NameNode)
		}
	}
}

func checkIdent(c *lint.PipelineContext, kind, name string, n *yaml.Node) {
	if name == "" || hasDynamic(name) || identRe.MatchString(name) {
		return
	}
	c.Report("ADO101", n, fmt.Sprintf("%s name %q is invalid: names may only contain letters, digits and '_' and may not start with a digit", kind, name))
}

type named struct {
	name string
	node *yaml.Node
	unit *yaml.Node
}

func checkDuplicates(c *lint.PipelineContext, kind, scope string, items []named) {
	seen := map[string]named{}
	for _, it := range items {
		if it.name == "" || hasDynamic(it.name) {
			continue
		}
		key := strings.ToLower(it.name)
		prev, dup := seen[key]
		if !dup {
			seen[key] = it
			continue
		}
		if c.Doc.Conditional(prev.unit) || c.Doc.Conditional(it.unit) {
			continue
		}
		loc := c.Doc.Loc(prev.node)
		c.Report("ADO102", it.node, fmt.Sprintf("duplicate %s name %q%s (first defined at %s:%d)", kind, it.name, scope, loc.File, loc.Line))
	}
}

func checkDuplicateNames(c *lint.PipelineContext) {
	var stages []named
	for _, s := range c.Pipeline.Stages {
		if !s.Implicit {
			stages = append(stages, named{s.Name, s.NameNode, s.Node})
		}
		var jobs []named
		for _, j := range s.Jobs {
			if !j.Implicit {
				jobs = append(jobs, named{j.Name, j.NameNode, j.Node})
			}
			var steps []named
			for _, st := range j.Steps {
				steps = append(steps, named{st.Name, st.NameNode, st.Node})
			}
			checkDuplicates(c, "step", " in this job", steps)
		}
		checkDuplicates(c, "job", " in this stage", jobs)
	}
	checkDuplicates(c, "stage", "", stages)
}

// depGraph is a dependency graph between stages or jobs.
type depGraph struct {
	names map[string]string
	edges map[string][]model.Ref
	order []string
}

func newGraph() *depGraph {
	return &depGraph{names: map[string]string{}, edges: map[string][]model.Ref{}}
}

func (g *depGraph) add(name string, deps []model.Ref) {
	k := strings.ToLower(name)
	if _, ok := g.names[k]; ok || name == "" {
		return
	}
	g.names[k] = name
	g.edges[k] = deps
	g.order = append(g.order, k)
}

func (g *depGraph) mayContain(name string) bool {
	if _, ok := g.names[strings.ToLower(name)]; ok {
		return true
	}
	for _, pattern := range g.names {
		if !hasDynamic(pattern) {
			continue
		}
		var b strings.Builder
		b.WriteString("(?i)^")
		last := 0
		spans, errs := expr.Scan(pattern)
		if len(errs) > 0 {
			return true
		}
		for _, sp := range spans {
			if sp.Start < last {
				continue
			}
			b.WriteString(regexp.QuoteMeta(pattern[last:sp.Start]))
			b.WriteString(".*")
			last = sp.End
		}
		b.WriteString(regexp.QuoteMeta(pattern[last:]))
		b.WriteString("$")
		if regexp.MustCompile(b.String()).MatchString(name) {
			return true
		}
	}
	return false
}

func checkUnknownDeps(c *lint.PipelineContext) { checkDependencies(c, false) }

func checkCycles(c *lint.PipelineContext) { checkDependencies(c, true) }

func checkDependencies(c *lint.PipelineContext, cycles bool) {
	p := c.Pipeline
	stages := newGraph()
	for _, s := range p.Stages {
		if !s.Implicit {
			stages.add(s.Name, s.DependsOn)
		}
		jobs := newGraph()
		for _, j := range s.Jobs {
			jobs.add(j.Name, j.DependsOn)
		}
		reportGraph(c, jobs, "job", p.Partial || c.Doc.Open(s.JobsNode), cycles)
	}
	reportGraph(c, stages, "stage", p.Partial || (p.StagesNode != nil && c.Doc.Open(p.StagesNode)), cycles)
}

func reportGraph(c *lint.PipelineContext, g *depGraph, kind string, open, cycles bool) {
	if cycles {
		reportCycles(c, g, kind)
		return
	}
	for _, k := range g.order {
		for _, d := range g.edges[k] {
			if g.mayContain(d.Name) || open || hasDynamic(d.Name) {
				continue
			}
			c.Report("ADO103", d.Node, fmt.Sprintf("%s %q depends on unknown %s %q", kind, g.names[k], kind, d.Name))
		}
	}
}

func reportCycles(c *lint.PipelineContext, g *depGraph, kind string) {
	reported := map[string]bool{}
	for _, k := range g.order {
		if cycle := findCycle(g, k); cycle != nil && !reported[cycle[0]] {
			for _, n := range cycle {
				reported[n] = true
			}
			names := make([]string, 0, len(cycle)+1)
			for _, n := range append(cycle, cycle[0]) {
				names = append(names, g.names[n])
			}
			c.Report("ADO104", depNode(g, cycle[0], cycle[(1)%len(cycle)]), fmt.Sprintf("%s dependency cycle: %s", kind, strings.Join(names, " -> ")))
		}
	}
}

func depNode(g *depGraph, from, to string) *yaml.Node {
	for _, d := range g.edges[from] {
		if strings.EqualFold(d.Name, to) {
			return d.Node
		}
	}
	return nil
}

// findCycle returns the cycle starting at start, if start is part of one.
func findCycle(g *depGraph, start string) []string {
	var path []string
	onPath := map[string]bool{}
	visited := map[string]bool{}
	var dfs func(n string) []string
	dfs = func(n string) []string {
		if onPath[n] {
			if n == start {
				return append([]string(nil), path...)
			}
			return nil
		}
		if visited[n] {
			return nil
		}
		visited[n], onPath[n] = true, true
		path = append(path, n)
		for _, d := range g.edges[n] {
			if c := dfs(strings.ToLower(d.Name)); c != nil {
				return c
			}
		}
		path = path[:len(path)-1]
		onPath[n] = false
		return nil
	}
	return dfs(start)
}

func checkTaskVersion(c *lint.PipelineContext) {
	for _, st := range c.Pipeline.AllSteps() {
		ref := st.TaskRef()
		if st.Kind != "task" || ref == "" || hasDynamic(ref) || strings.Contains(ref, "@") {
			continue
		}
		msg := fmt.Sprintf("task %q has no version; pin a major version like %s@<major>", ref, ref)
		if vers := c.Schema.TaskVersions(ref); len(vers) > 0 {
			msg += fmt.Sprintf(" (known versions: %s)", strings.Join(vers, ", "))
		}
		c.Report("ADO105", st.Value, msg)
	}
}
