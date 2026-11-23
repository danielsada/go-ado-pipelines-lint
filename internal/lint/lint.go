// Package lint runs rules over parsed and expanded pipelines and applies configuration.
package lint

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/danielsada/go-ado-pipelines-lint/internal/diag"
	"github.com/danielsada/go-ado-pipelines-lint/internal/model"
	"github.com/danielsada/go-ado-pipelines-lint/internal/schema"
	"github.com/danielsada/go-ado-pipelines-lint/internal/template"
	"github.com/danielsada/go-ado-pipelines-lint/internal/yamlx"
)

// DocsBaseURL is where the generated rule documentation is published (GitHub Pages).
const DocsBaseURL = "https://danielsada.github.io/go-ado-pipelines-lint/"

// Meta describes a rule.
type Meta struct {
	ID       string
	Name     string
	Severity diag.Severity
	// DisabledByDefault rules only run when enabled in config or with --enable.
	DisabledByDefault bool
	Summary           string
	Description       string
	Bad               string
	Good              string
	// HelpURI links to the upstream (Azure Pipelines, ShellCheck, ...) documentation the rule is based on.
	HelpURI string
}

// DocsURL returns the URL of the rule's documentation page.
func (m *Meta) DocsURL() string { return DocsURL(m.ID) }

// DocsURL returns the documentation page for a rule ID. Sub-rule IDs such as ADO701/SC2086 link to
// their parent rule.
func DocsURL(id string) string {
	if i := strings.IndexByte(id, '/'); i >= 0 {
		id = id[:i]
	}
	return DocsBaseURL + "rules/" + id + "/"
}

// IssuesURL is where questions and false-positive reports go.
const IssuesURL = "https://github.com/danielsada/go-ado-pipelines-lint/issues"

// Category returns the rule's group, derived from its ID.
func (m *Meta) Category() string {
	if len(m.ID) < 4 {
		return "Other"
	}
	switch m.ID[3] {
	case '0':
		return "Syntax and schema"
	case '1':
		return "Structure and identity"
	case '2':
		return "Templates and parameters"
	case '3':
		return "Expressions"
	case '4':
		return "Best practices"
	case '5':
		return "Tasks"
	case '6':
		return "Conditions"
	case '7':
		return "Scripts"
	}
	return "Other"
}

// Rule is anything with metadata. Rules without a checker document diagnostics
// emitted by the parser, expander, schema validator or script linters.
type Rule interface {
	Meta() *Meta
}

// SourceRule checks a single source file before template expansion.
type SourceRule interface {
	Rule
	CheckSource(*SourceContext)
}

// PipelineRule checks an expanded pipeline.
type PipelineRule interface {
	Rule
	CheckPipeline(*PipelineContext)
}

// Registry is a set of rules indexed by ID.
type Registry struct {
	rules []Rule
	byID  map[string]Rule
}

// NewRegistry creates a registry. Duplicate IDs panic.
func NewRegistry(rules ...Rule) *Registry {
	r := &Registry{byID: map[string]Rule{}}
	for _, rule := range rules {
		id := rule.Meta().ID
		if _, dup := r.byID[id]; dup {
			panic(fmt.Sprintf("duplicate rule %s", id))
		}
		r.byID[id] = rule
		r.rules = append(r.rules, rule)
	}
	return r
}

// All returns all rules in registration order.
func (r *Registry) All() []Rule { return r.rules }

// Get returns a rule by ID.
func (r *Registry) Get(id string) (Rule, bool) {
	rule, ok := r.byID[id]
	return rule, ok
}

// Has reports whether a rule ID exists.
func (r *Registry) Has(id string) bool {
	_, ok := r.byID[id]
	return ok
}

type reporter struct {
	reg   *Registry
	diags *[]diag.Diagnostic
}

func (r reporter) add(id string, loc diag.Location, msg string) {
	sev := diag.Warning
	if rule, ok := r.reg.Get(id); ok {
		sev = rule.Meta().Severity
	}
	*r.diags = append(*r.diags, diag.Diagnostic{RuleID: id, Severity: sev, Message: msg, Location: loc})
}

// SourceContext is passed to source rules.
type SourceContext struct {
	Src *yamlx.Source
	// IsTemplate is true if the file is linted as a template (included by another file or template-shaped).
	IsTemplate bool
	// IsEntry is true if the file is linted as a pipeline entry point.
	IsEntry bool
	Params  []*template.Param
	rep     reporter
}

// Report emits a diagnostic at a source node.
func (c *SourceContext) Report(id string, n *yaml.Node, msg string) {
	c.ReportAt(id, c.Loc(n), msg)
}

// ReportAt emits a diagnostic at a location.
func (c *SourceContext) ReportAt(id string, loc diag.Location, msg string) { c.rep.add(id, loc, msg) }

// Loc returns the location of a node in this source.
func (c *SourceContext) Loc(n *yaml.Node) diag.Location {
	if n == nil {
		return diag.Location{File: c.Src.Path, Line: 1, Column: 1}
	}
	return diag.Location{File: c.Src.Path, Line: n.Line, Column: n.Column}
}

// PipelineContext is passed to pipeline rules.
type PipelineContext struct {
	Pipeline *model.Pipeline
	Doc      *yamlx.Doc
	Schema   *schema.Validator
	rep      reporter
}

// Report emits a diagnostic at an expanded node (located at its origin).
func (c *PipelineContext) Report(id string, n *yaml.Node, msg string) {
	c.rep.add(id, c.Doc.Loc(n), msg)
}
