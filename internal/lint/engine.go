package lint

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/danielsada/go-ado-pipelines-lint/internal/config"
	"github.com/danielsada/go-ado-pipelines-lint/internal/diag"
	"github.com/danielsada/go-ado-pipelines-lint/internal/expr"
	"github.com/danielsada/go-ado-pipelines-lint/internal/model"
	"github.com/danielsada/go-ado-pipelines-lint/internal/schema"
	"github.com/danielsada/go-ado-pipelines-lint/internal/scripts"
	"github.com/danielsada/go-ado-pipelines-lint/internal/template"
	"github.com/danielsada/go-ado-pipelines-lint/internal/yamlx"
)

// Input is a file to lint.
type Input struct {
	// Abs is the absolute path.
	Abs string
	// Path is the display path used in diagnostics.
	Path string
}

// Options configures an engine.
type Options struct {
	Registry *Registry
	Config   *config.Config
	// Overrides are per-rule severities from the command line; they take precedence over Config.
	Overrides map[string]diag.Severity
	// RepoRoot resolves template paths starting with "/".
	RepoRoot             string
	NoTemplates          bool
	NoScripts            bool
	RequireScriptLinters bool
	Runner               scripts.Runner
	ReadFile             func(string) ([]byte, error)
	Display              func(string) string
}

// Engine lints pipelines.
type Engine struct {
	opts      Options
	loader    *template.Loader
	validator *schema.Validator
}

// Entry is a file linted as an entry point (a pipeline, or a stand-alone template).
type Entry struct {
	Src        *yamlx.Source
	IsTemplate bool
	Result     *template.Result
	Model      *model.Pipeline
}

// New creates an engine.
func New(opts Options) (*Engine, error) {
	if opts.Config == nil {
		opts.Config = config.Default(".")
	}
	if opts.ReadFile == nil {
		opts.ReadFile = os.ReadFile
	}
	if opts.Display == nil {
		opts.Display = func(s string) string { return s }
	}
	if opts.Runner == nil {
		opts.Runner = scripts.ExecRunner{}
	}
	v, err := schema.NewValidator()
	if err != nil {
		return nil, err
	}
	return &Engine{opts: opts, loader: template.NewLoader(opts.ReadFile, opts.Display), validator: v}, nil
}

// Load parses inputs and decides which are entry points. Files referenced as templates by
// other inputs are not entry points; template-shaped files are linted as templates.
func (e *Engine) Load(inputs []Input) (entries []*Entry, sources []*yamlx.Source, err error) {
	for _, in := range inputs {
		data, err := e.opts.ReadFile(in.Abs)
		if err != nil {
			return nil, nil, err
		}
		src, _ := yamlx.Parse(in.Path, in.Abs, data)
		e.loader.Add(src)
		sources = append(sources, src)
	}
	referenced := map[string]bool{}
	for _, src := range sources {
		for _, ref := range e.templateRefs(src) {
			if ref != src.Abs {
				referenced[ref] = true
			}
		}
	}
	for _, src := range sources {
		if referenced[src.Abs] {
			continue
		}
		entries = append(entries, &Entry{Src: src, IsTemplate: LooksLikeTemplate(src)})
	}
	return entries, sources, nil
}

// Expand expands an entry and builds its model.
func (e *Engine) Expand(en *Entry) {
	opts := template.Options{RepoRoot: e.opts.RepoRoot, Loader: e.loader, NoTemplates: e.opts.NoTemplates}
	if en.IsTemplate {
		en.Result = template.ExpandTemplate(en.Src, opts)
	} else {
		en.Result = template.ExpandPipeline(en.Src, opts)
	}
	en.Model = model.Build(en.Result.Doc, en.IsTemplate)
	en.Model.Partial = en.IsTemplate
}

// Validator returns the schema validator.
func (e *Engine) Validator() *schema.Validator { return e.validator }

// Lint lints the inputs. The returned error is non-nil only for --require-script-linters failures.
func (e *Engine) Lint(ctx context.Context, inputs []Input) ([]diag.Diagnostic, error) {
	entries, inputSources, err := e.Load(inputs)
	if err != nil {
		return nil, err
	}
	var diags []diag.Diagnostic
	rep := reporter{reg: e.opts.Registry, diags: &diags}
	var all []*yamlx.Source
	seen := map[*yamlx.Source]bool{}
	addSrc := func(s *yamlx.Source) {
		if !seen[s] {
			seen[s] = true
			all = append(all, s)
		}
	}
	templates := map[*yamlx.Source]bool{}
	extractor := scripts.NewExtractor(scripts.Options{ScriptShell: e.opts.Config.Scripts.ScriptShell})
	for _, en := range entries {
		e.Expand(en)
		diags = append(diags, en.Result.Diags...)
		for _, m := range append([]*model.Pipeline{en.Model}, ExtendsParameterPipelines(en)...) {
			e.checkPipeline(&PipelineContext{Pipeline: m, Doc: m.Doc, Schema: e.validator, rep: rep})
			if e.scriptsEnabled() {
				extractor.Add(m)
			}
		}
		for i, s := range en.Result.Doc.Sources {
			if i > 0 || en.IsTemplate {
				templates[s] = true
			}
			addSrc(s)
		}
	}
	for _, s := range inputSources {
		addSrc(s)
	}
	entrySet := map[*yamlx.Source]bool{}
	for _, en := range entries {
		entrySet[en.Src] = !en.IsTemplate
	}
	for _, s := range all {
		diags = append(diags, s.ParseDiags...)
		if s.Root != nil {
			e.checkSource(&SourceContext{Src: s, IsTemplate: templates[s] || !entrySet[s] && LooksLikeTemplate(s),
				IsEntry: entrySet[s], Params: template.DeclaredParams(s.Root), rep: rep})
		}
	}
	var toolErr error
	if found := extractor.Scripts(); e.scriptsEnabled() && len(found) > 0 {
		sd, err := scripts.Lint(ctx, e.opts.Runner, found, scripts.LintOptions{
			ShellCheck: e.opts.Config.Scripts.ShellCheck, PSScriptAnalyzer: e.opts.Config.Scripts.PSScriptAnalyzer,
			Require: e.opts.RequireScriptLinters})
		diags = append(diags, sd...)
		if errors.Is(err, scripts.ErrRequiredToolMissing) {
			toolErr = err
		}
	}
	return e.finalize(diags, all), toolErr
}

func (e *Engine) scriptsEnabled() bool {
	return !e.opts.NoScripts && e.opts.Config.ScriptsEnabled()
}

func (e *Engine) checkPipeline(c *PipelineContext) {
	e.validator.Open = c.Doc.Open
	e.validator.Conditional = c.Doc.Conditional
	e.checkSchema(c)
	for _, r := range e.opts.Registry.All() {
		if pr, ok := r.(PipelineRule); ok && e.Enabled(r.Meta().ID) {
			pr.CheckPipeline(c)
		}
	}
}

// SchemaRuleIDs are the rules reported by schema validation.
var SchemaRuleIDs = []string{"ADO003", "ADO005", "ADO006", "ADO007", "ADO501", "ADO502", "ADO503"}

func (e *Engine) checkSchema(c *PipelineContext) {
	def := SchemaDef(c.Pipeline)
	if def == "" || c.Doc.Root == nil {
		return
	}
	enabled := false
	for _, id := range SchemaRuleIDs {
		enabled = enabled || e.Enabled(id)
	}
	if !enabled {
		return
	}
	for _, err := range e.validator.Validate(c.Doc.Root, def) {
		c.rep.add(err.RuleID(), c.Doc.Loc(err.Node), err.Message)
	}
}

// SchemaDef returns the schema definition a pipeline or template root is validated against.
func SchemaDef(p *model.Pipeline) string {
	if !p.IsTemplate {
		return "pipeline"
	}
	for _, k := range []string{"stages", "jobs", "steps", "variables"} {
		if yamlx.Has(p.Root, k) {
			if k == "stages" {
				// Stage templates are often extends templates, which may also define variables,
				// resources and triggers.
				return "extendsTemplate"
			}
			return k + "Template"
		}
	}
	return ""
}

func (e *Engine) checkSource(c *SourceContext) {
	for _, r := range e.opts.Registry.All() {
		if sr, ok := r.(SourceRule); ok && e.Enabled(r.Meta().ID) {
			sr.CheckSource(c)
		}
	}
}

// configured returns the configured severity for a rule (CLI overrides first, then config).
func (e *Engine) configured(ids ...string) (diag.Severity, bool) {
	for _, m := range []map[string]diag.Severity{e.opts.Overrides, e.opts.Config.Rules} {
		for _, id := range ids {
			if s, ok := m[id]; ok {
				return s, true
			}
		}
	}
	return diag.Off, false
}

// Enabled reports whether a rule is enabled after configuration.
func (e *Engine) Enabled(id string) bool {
	if s, ok := e.configured(id); ok {
		return s != diag.Off
	}
	r, ok := e.opts.Registry.Get(id)
	return !ok || !r.Meta().DisabledByDefault
}

// finalize applies severities, suppressions and ignore patterns, then de-duplicates and sorts.
func (e *Engine) finalize(diags []diag.Diagnostic, sources []*yamlx.Source) []diag.Diagnostic {
	sup := map[string]*suppressions{}
	abs := map[string]string{}
	for _, s := range sources {
		sup[s.Path] = parseSuppressions(s)
		abs[s.Path] = s.Abs
	}
	out := make([]diag.Diagnostic, 0, len(diags))
	for _, d := range diags {
		if sev, ok := e.configured(d.FullID(), d.RuleID); ok {
			if sev == diag.Off {
				continue
			}
			d.Severity = sev
		} else if !e.Enabled(d.RuleID) {
			continue
		}
		if sup[d.File].suppressed(d) || e.ignored(abs[d.File]) {
			continue
		}
		out = append(out, d)
	}
	out = diag.Dedupe(out)
	diag.Sort(out)
	return onePerMessage(out, "ADO202")
}

// onePerMessage keeps only the first diagnostic of a rule for each distinct message, e.g. one
// "template not resolved" note per remote template across all linted pipelines.
func onePerMessage(ds []diag.Diagnostic, rule string) []diag.Diagnostic {
	seen := map[string]bool{}
	out := ds[:0]
	for _, d := range ds {
		if d.RuleID == rule {
			if seen[d.Message] {
				continue
			}
			seen[d.Message] = true
		}
		out = append(out, d)
	}
	return out
}

func (e *Engine) ignored(abs string) bool {
	if abs == "" || len(e.opts.Config.Ignore) == 0 {
		return false
	}
	rel, err := filepath.Rel(e.opts.Config.Dir, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return false
	}
	return e.opts.Config.Ignored(rel)
}

// templateRefs returns the absolute paths of local YAML files that src may reference as templates:
// template: values, and any other scalar naming a .yml/.yaml file (templates are often passed
// around as parameters, e.g. jobTemplate: /eng/pipelines/build-job.yml).
func (e *Engine) templateRefs(src *yamlx.Source) []string {
	var out []string
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		n = yamlx.Deref(n)
		if n == nil {
			return
		}
		if n.Kind == yaml.ScalarNode {
			out = append(out, e.refCandidates(src, n.Value)...)
			return
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	walk(src.Root)
	return out
}

func (e *Engine) refCandidates(src *yamlx.Source, v string) []string {
	v = strings.TrimSpace(v)
	p, alias := template.SplitRef(v)
	lp := strings.ToLower(p)
	if expr.HasDynamic(v) || strings.ContainsAny(p, " \n") || (!strings.HasSuffix(lp, ".yml") && !strings.HasSuffix(lp, ".yaml")) {
		return nil
	}
	if alias != "" && !strings.EqualFold(alias, "self") {
		return nil
	}
	out := []string{e.resolve(src, p)}
	if e.opts.RepoRoot != "" {
		out = append(out, filepath.Join(e.opts.RepoRoot, filepath.FromSlash(p)))
	}
	return out
}

func (e *Engine) resolve(src *yamlx.Source, p string) string {
	p = filepath.FromSlash(p)
	if strings.HasPrefix(p, string(filepath.Separator)) {
		return filepath.Join(e.opts.RepoRoot, p)
	}
	return filepath.Join(filepath.Dir(src.Abs), p)
}

var pipelineOnlyKeys = []string{"trigger", "pr", "schedules", "resources", "pool", "name", "extends",
	"lockBehavior", "appendCommitMessageToRunName"}

// LooksLikeTemplate reports whether a file looks like a template rather than a pipeline. It must have
// one of stages/jobs/steps (or only variables) and no pipeline-only keys such as trigger or pool, and
// additionally declare parameters, use parameters, or live under a directory named like "templates".
func LooksLikeTemplate(src *yamlx.Source) bool {
	root := src.Root
	if !yamlx.IsMap(root) {
		return false
	}
	for _, k := range pipelineOnlyKeys {
		if yamlx.Has(root, k) {
			return false
		}
	}
	body := 0
	for _, k := range []string{"stages", "jobs", "steps"} {
		if yamlx.Has(root, k) {
			body++
		}
	}
	if body == 0 {
		return yamlx.Has(root, "variables")
	}
	return body == 1 && (yamlx.Has(root, "parameters") || usesParameters(src) || inTemplateDir(src))
}

func usesParameters(src *yamlx.Source) bool {
	text := string(src.Data)
	return strings.Contains(text, "parameters.") || strings.Contains(text, "parameters[") || strings.Contains(text, "in parameters")
}

func inTemplateDir(src *yamlx.Source) bool {
	for _, part := range strings.Split(filepath.ToSlash(filepath.Dir(src.Path)), "/") {
		if strings.Contains(strings.ToLower(part), "template") {
			return true
		}
	}
	return false
}

// ExtendsParameterPipelines returns virtual pipelines for stages, jobs or steps that an entry passes as
// parameters to an extends template that could not be expanded (for example a template in another
// repository such as the 1ES or OneBranch templates). They are linted like a template body, with an
// unknown default pool unless a pool parameter is given.
func ExtendsParameterPipelines(en *Entry) []*model.Pipeline {
	doc := en.Result.Doc
	_, ev := yamlx.Get(doc.Root, "extends")
	params := yamlx.Value(ev, "parameters")
	var out []*model.Pipeline
	yamlx.Pairs(params, func(k, v *yaml.Node) {
		kind := bodyKind(v)
		if kind == "" {
			return
		}
		root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Line: k.Line, Column: k.Column,
			Content: []*yaml.Node{yamlx.Scalar(kind, k), v}}
		vdoc := doc.WithRoot(root)
		vdoc.SetOrigin(root, doc.Main, k)
		vdoc.SetOrigin(root.Content[0], doc.Main, k)
		m := model.Build(vdoc, true)
		m.Pool = model.PoolOf(yamlx.Value(params, "pool"))
		m.PoolUnknown = true
		// The remote template may add stages or jobs (such as SDL stages) these can depend on.
		m.Partial = true
		out = append(out, m)
	})
	return out
}

// bodyKind classifies a parameter value as a list of stages, jobs or steps.
func bodyKind(v *yaml.Node) string {
	for _, it := range yamlx.Items(v) {
		switch {
		case yamlx.Has(it, "stage"):
			return "stages"
		case yamlx.Has(it, "job"), yamlx.Has(it, "deployment"):
			return "jobs"
		}
		for _, k := range model.StepKinds {
			if k != "template" && yamlx.Has(it, k) {
				return "steps"
			}
		}
	}
	return ""
}
