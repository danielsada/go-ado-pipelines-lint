// Package model builds a typed, position-aware view of an (expanded) Azure Pipelines document.
// Shorthand forms are normalized: pipelines with only jobs or steps get an implicit stage
// (and job), pools may be strings or mappings, and dependsOn may be a string or a list.
package model

import (
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/danielsada/go-ado-pipelines-lint/internal/yamlx"
)

// Pipeline is the root of the model.
type Pipeline struct {
	Doc  *yamlx.Doc
	Root *yaml.Node
	// IsTemplate is true when the file was linted as a template rather than a pipeline.
	IsTemplate bool
	Stages     []*Stage
	// StagesNode is the stages sequence, or nil if stages are implicit.
	StagesNode *yaml.Node
	Pool       *Pool
	Variables  []*Variable
}

// Ref is a reference by name to another stage or job.
type Ref struct {
	Name string
	Node *yaml.Node
}

// Stage is a stage (possibly implicit).
type Stage struct {
	Name      string
	NameNode  *yaml.Node
	Node      *yaml.Node
	Implicit  bool
	Index     int
	DependsOn []Ref
	// HasDependsOn is true if dependsOn was specified (even if empty).
	HasDependsOn bool
	Condition    *yaml.Node
	Pool         *Pool
	Variables    []*Variable
	Jobs         []*Job
	JobsNode     *yaml.Node
	Pipeline     *Pipeline
}

// Job is a job or deployment job (possibly implicit).
type Job struct {
	Name         string
	NameNode     *yaml.Node
	Node         *yaml.Node
	Deployment   bool
	Implicit     bool
	DependsOn    []Ref
	HasDependsOn bool
	Condition    *yaml.Node
	Pool         *Pool
	Container    *yaml.Node
	Variables    []*Variable
	MatrixVars   []string
	Steps        []*Step
	StepsNodes   []*yaml.Node
	Stage        *Stage
}

// Step is a single step.
type Step struct {
	// Kind is the step's keyword: task, script, bash, pwsh, powershell, checkout, ...
	Kind      string
	KindKey   *yaml.Node
	Value     *yaml.Node
	Node      *yaml.Node
	Name      string
	NameNode  *yaml.Node
	Condition *yaml.Node
	Inputs    *yaml.Node
	Env       *yaml.Node
	Job       *Job
}

// Variable is a variable definition, variable group or variable template reference.
type Variable struct {
	Name     string
	NameNode *yaml.Node
	Value    *yaml.Node
	Group    string
	Template string
	Node     *yaml.Node
	ReadOnly bool
}

// Pool is a pool specification.
type Pool struct {
	Node        *yaml.Node
	Name        string
	VMImage     string
	VMImageNode *yaml.Node
}

// StepKinds lists the step keywords, in the order used to classify a step.
var StepKinds = []string{"task", "script", "bash", "pwsh", "powershell", "checkout", "download",
	"downloadBuild", "getPackage", "publish", "upload", "template", "reviewApp"}

// Build creates the model from an expanded document.
func Build(doc *yamlx.Doc, isTemplate bool) *Pipeline {
	p := &Pipeline{Doc: doc, Root: doc.Root, IsTemplate: isTemplate}
	root := doc.Root
	if !yamlx.IsMap(root) {
		return p
	}
	p.Pool = buildPool(yamlx.Value(root, "pool"))
	p.Variables = buildVariables(yamlx.Value(root, "variables"))
	switch {
	case yamlx.Has(root, "stages"):
		p.StagesNode = yamlx.Value(root, "stages")
		for _, it := range yamlx.Items(p.StagesNode) {
			if yamlx.Has(it, "stage") {
				p.Stages = append(p.Stages, buildStage(p, it, len(p.Stages)))
			}
		}
	case yamlx.Has(root, "jobs"):
		s := &Stage{Node: root, Implicit: true, Pipeline: p}
		s.JobsNode = yamlx.Value(root, "jobs")
		s.Jobs = buildJobs(s, s.JobsNode)
		p.Stages = []*Stage{s}
	case yamlx.Has(root, "steps"):
		s := &Stage{Node: root, Implicit: true, Pipeline: p}
		j := &Job{Node: root, Implicit: true, Stage: s}
		addSteps(j, yamlx.Value(root, "steps"))
		s.Jobs = []*Job{j}
		p.Stages = []*Stage{s}
	}
	return p
}

func refs(n *yaml.Node) []Ref {
	names, nodes := yamlx.StringList(n)
	out := make([]Ref, 0, len(names))
	for i, name := range names {
		if strings.TrimSpace(name) != "" {
			out = append(out, Ref{Name: name, Node: nodes[i]})
		}
	}
	return out
}

func buildStage(p *Pipeline, n *yaml.Node, idx int) *Stage {
	_, nameNode := yamlx.Get(n, "stage")
	s := &Stage{Node: n, NameNode: nameNode, Index: idx, Pipeline: p}
	if yamlx.IsScalar(nameNode) {
		s.Name = nameNode.Value
	}
	if k, v := yamlx.Get(n, "dependsOn"); k != nil {
		s.HasDependsOn = true
		s.DependsOn = refs(v)
	}
	s.Condition = yamlx.Value(n, "condition")
	s.Pool = buildPool(yamlx.Value(n, "pool"))
	s.Variables = buildVariables(yamlx.Value(n, "variables"))
	s.JobsNode = yamlx.Value(n, "jobs")
	s.Jobs = buildJobs(s, s.JobsNode)
	return s
}

func buildJobs(s *Stage, seq *yaml.Node) []*Job {
	var jobs []*Job
	for _, it := range yamlx.Items(seq) {
		key := "job"
		if yamlx.Has(it, "deployment") {
			key = "deployment"
		} else if !yamlx.Has(it, "job") {
			continue
		}
		_, nameNode := yamlx.Get(it, key)
		j := &Job{Node: it, NameNode: nameNode, Deployment: key == "deployment", Stage: s}
		if yamlx.IsScalar(nameNode) && !yamlx.IsNull(nameNode) {
			j.Name = nameNode.Value
		}
		if k, v := yamlx.Get(it, "dependsOn"); k != nil {
			j.HasDependsOn = true
			j.DependsOn = refs(v)
		}
		j.Condition = yamlx.Value(it, "condition")
		j.Pool = buildPool(yamlx.Value(it, "pool"))
		j.Container = yamlx.Value(it, "container")
		j.Variables = buildVariables(yamlx.Value(it, "variables"))
		strategy := yamlx.Value(it, "strategy")
		yamlx.Pairs(yamlx.Value(strategy, "matrix"), func(_, leg *yaml.Node) {
			yamlx.Pairs(leg, func(k, _ *yaml.Node) { j.MatrixVars = append(j.MatrixVars, k.Value) })
		})
		if j.Deployment {
			collectSteps(j, strategy, 0)
		} else {
			addSteps(j, yamlx.Value(it, "steps"))
		}
		jobs = append(jobs, j)
	}
	return jobs
}

// collectSteps finds the steps lists inside a deployment strategy (deploy, preDeploy, on.failure, ...).
func collectSteps(j *Job, n *yaml.Node, depth int) {
	if depth > 6 {
		return
	}
	yamlx.Pairs(n, func(k, v *yaml.Node) {
		if k.Value == "steps" && yamlx.IsSeq(v) {
			addSteps(j, v)
			return
		}
		if yamlx.IsMap(v) {
			collectSteps(j, v, depth+1)
		}
	})
}

func addSteps(j *Job, seq *yaml.Node) {
	if seq == nil {
		return
	}
	j.StepsNodes = append(j.StepsNodes, seq)
	for _, it := range yamlx.Items(seq) {
		if st := buildStep(j, it); st != nil {
			j.Steps = append(j.Steps, st)
		}
	}
}

func buildStep(j *Job, n *yaml.Node) *Step {
	if !yamlx.IsMap(n) {
		return nil
	}
	for _, kind := range StepKinds {
		k, v := yamlx.Get(n, kind)
		if k == nil {
			continue
		}
		st := &Step{Kind: kind, KindKey: k, Value: v, Node: n, Job: j}
		_, st.NameNode = yamlx.Get(n, "name")
		if yamlx.IsScalar(st.NameNode) {
			st.Name = st.NameNode.Value
		}
		st.Condition = yamlx.Value(n, "condition")
		st.Inputs = yamlx.Value(n, "inputs")
		st.Env = yamlx.Value(n, "env")
		return st
	}
	return nil
}

// TaskRef returns the task reference (e.g. "Bash@3") for task steps.
func (s *Step) TaskRef() string {
	if s.Kind != "task" || !yamlx.IsScalar(s.Value) {
		return ""
	}
	return strings.TrimSpace(s.Value.Value)
}

// TaskName returns the task name without version, lower-cased, and the major version string.
func (s *Step) TaskName() (name, version string) {
	ref := s.TaskRef()
	name, version, _ = strings.Cut(ref, "@")
	return strings.ToLower(name), version
}

// Input returns the value node of a task input (case-insensitive).
func (s *Step) Input(name string) *yaml.Node {
	_, v := yamlx.GetFold(s.Inputs, name)
	return v
}

// InputStr returns the scalar value of a task input, or "".
func (s *Step) InputStr(name string) string {
	v := s.Input(name)
	if !yamlx.IsScalar(v) {
		return ""
	}
	return v.Value
}

func buildPool(n *yaml.Node) *Pool {
	if n == nil {
		return nil
	}
	p := &Pool{Node: n}
	if yamlx.IsScalar(n) {
		p.Name = n.Value
		return p
	}
	p.Name = yamlx.Str(n, "name")
	_, p.VMImageNode = yamlx.Get(n, "vmImage")
	if yamlx.IsScalar(p.VMImageNode) {
		p.VMImage = p.VMImageNode.Value
	}
	return p
}

func buildVariables(n *yaml.Node) []*Variable {
	var out []*Variable
	if yamlx.IsMap(n) {
		yamlx.Pairs(n, func(k, v *yaml.Node) {
			out = append(out, &Variable{Name: k.Value, NameNode: k, Value: v, Node: k})
		})
		return out
	}
	for _, it := range yamlx.Items(n) {
		v := &Variable{Node: it, Group: yamlx.Str(it, "group"), Template: yamlx.Str(it, "template")}
		if _, nn := yamlx.Get(it, "name"); yamlx.IsScalar(nn) {
			v.Name, v.NameNode = nn.Value, nn
		}
		v.Value = yamlx.Value(it, "value")
		v.ReadOnly = strings.EqualFold(yamlx.Str(it, "readonly"), "true")
		out = append(out, v)
	}
	return out
}

// EffectivePool returns the pool a job runs on (job, then stage, then pipeline), or nil.
func (j *Job) EffectivePool() *Pool {
	if j.Pool != nil {
		return j.Pool
	}
	if j.Stage != nil && j.Stage.Pool != nil {
		return j.Stage.Pool
	}
	if j.Stage != nil && j.Stage.Pipeline != nil {
		return j.Stage.Pipeline.Pool
	}
	return nil
}

// AllJobs returns every job in the pipeline.
func (p *Pipeline) AllJobs() []*Job {
	var out []*Job
	for _, s := range p.Stages {
		out = append(out, s.Jobs...)
	}
	return out
}

// AllSteps returns every step in the pipeline.
func (p *Pipeline) AllSteps() []*Step {
	var out []*Step
	for _, j := range p.AllJobs() {
		out = append(out, j.Steps...)
	}
	return out
}

// DeclaredVariableNames returns all variable names declared anywhere in the pipeline,
// including matrix variables, lower-cased.
func (p *Pipeline) DeclaredVariableNames() map[string]bool {
	names := map[string]bool{}
	add := func(vs []*Variable) {
		for _, v := range vs {
			if v.Name != "" {
				names[strings.ToLower(v.Name)] = true
			}
		}
	}
	add(p.Variables)
	for _, s := range p.Stages {
		add(s.Variables)
		for _, j := range s.Jobs {
			add(j.Variables)
			for _, m := range j.MatrixVars {
				names[strings.ToLower(m)] = true
			}
		}
	}
	return names
}
