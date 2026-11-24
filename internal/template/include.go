package template

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/danielsada/go-ado-pipelines-lint/internal/diag"
	"github.com/danielsada/go-ado-pipelines-lint/internal/expr"
	"github.com/danielsada/go-ado-pipelines-lint/internal/yamlx"
)

// Loader reads and caches template files.
type Loader struct {
	// ReadFile reads a file by absolute path.
	ReadFile func(string) ([]byte, error)
	// Display converts an absolute path to the path shown in diagnostics.
	Display func(string) string
	cache   map[string]loadResult
}

type loadResult struct {
	src *yamlx.Source
	err error
}

// NewLoader creates a loader.
func NewLoader(read func(string) ([]byte, error), display func(string) string) *Loader {
	if display == nil {
		display = func(s string) string { return s }
	}
	return &Loader{ReadFile: read, Display: display, cache: map[string]loadResult{}}
}

// Load parses the file at abs (cached).
func (l *Loader) Load(abs string) (*yamlx.Source, error) {
	if r, ok := l.cache[abs]; ok {
		return r.src, r.err
	}
	data, err := l.ReadFile(abs)
	var src *yamlx.Source
	if err == nil {
		src, _ = yamlx.Parse(l.Display(abs), abs, data)
	}
	l.cache[abs] = loadResult{src, err}
	return src, err
}

// Add registers an already parsed source so it is shared with template references.
func (l *Loader) Add(src *yamlx.Source) {
	if src.Abs != "" {
		l.cache[src.Abs] = loadResult{src: src}
	}
}

var bodyKeys = []string{"stages", "jobs", "steps", "variables"}

func isBodyKey(k string) bool {
	for _, b := range bodyKeys {
		if b == k {
			return true
		}
	}
	return false
}

// SplitRef splits "path@repo" into the path and repository alias.
func SplitRef(ref string) (path, alias string) {
	if i := strings.LastIndex(ref, "@"); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	return ref, ""
}

// resolveRef evaluates a template reference and loads the file. ok is false if it was not resolved.
func (e *expander) resolveRef(tv *yaml.Node, f *frame) (*yamlx.Source, *frame, bool) {
	if e.opts.NoTemplates || e.opts.Loader == nil || !yamlx.IsScalar(tv) {
		return nil, nil, false
	}
	ref := strings.TrimSpace(e.interpolate(tv.Value, f))
	path, alias := SplitRef(ref)
	if alias != "" && slices.ContainsFunc(e.opts.IgnoreRepositories, func(ignored string) bool { return strings.EqualFold(alias, ignored) }) {
		return nil, nil, false
	}
	if ref == "" || expr.HasDynamic(ref) {
		e.emit(RuleUnresolved, diag.Info, e.at(tv, f), fmt.Sprintf("template reference %q cannot be resolved statically; its contents are not linted", tv.Value))
		return nil, nil, false
	}
	repoRoot := f.repoRoot
	if strings.EqualFold(alias, "self") {
		repoRoot = e.opts.RepoRoot
	} else if alias != "" {
		repoRoot = e.opts.Repositories[strings.ToLower(alias)]
	}
	if alias != "" && repoRoot == "" && !strings.EqualFold(alias, "self") {
		if e.remote[strings.ToLower(ref)] {
			return nil, nil, false
		}
		e.remote[strings.ToLower(ref)] = true
		e.emit(RuleUnresolved, diag.Info, e.at(tv, f), fmt.Sprintf("template %q is in repository %q without a local checkout mapping; its contents are not linted", path, alias))
		return nil, nil, false
	}
	abs := e.locate(path, alias, f)
	if !e.checkLimits(abs, tv, f) {
		return nil, nil, false
	}
	src, err := e.opts.Loader.Load(abs)
	if err != nil {
		msg := fmt.Sprintf("cannot read template %q: %v", path, err)
		if errors.Is(err, fs.ErrNotExist) {
			msg = fmt.Sprintf("template file %q not found (resolved to %s)", path, e.opts.Loader.Display(abs))
		}
		e.emit(RuleTemplateRef, diag.Error, e.at(tv, f), msg)
		return nil, nil, false
	}
	e.doc.AddSource(src)
	if src.Invalid {
		return nil, nil, false
	}
	stack := append(append([]string(nil), f.stack...), abs)
	return src, &frame{src: src, ctx: expr.NewContext(), depth: f.depth + 1, stack: stack, cond: f.cond, repoRoot: repoRoot, parent: f}, true
}

// locate resolves a template path. Paths are relative to the including file (or to the repository
// root if they start with /). A path@self reference that doesn't exist relative to the file is retried
// from the repository root: @self paths inside parameters handed to another repository's template are
// evaluated there, relative to the root of this repository.
func (e *expander) locate(path, alias string, f *frame) string {
	opts := e.opts
	opts.SourceRoot = f.repoRoot
	return opts.ResolvePath(path, alias, f.src)
}

// ResolvePath resolves a static reference using the same checkout rules as expansion.
func (o Options) ResolvePath(path, alias string, src *yamlx.Source) string {
	currentRoot := o.SourceRoot
	if currentRoot == "" {
		currentRoot = o.RepoRoot
	}
	if alias != "" && !strings.EqualFold(alias, "self") {
		return filepath.Join(o.Repositories[strings.ToLower(alias)], filepath.FromSlash(path))
	}
	if strings.EqualFold(alias, "self") && currentRoot != o.RepoRoot {
		return filepath.Join(o.RepoRoot, filepath.FromSlash(path))
	}
	p := filepath.FromSlash(path)
	abs := filepath.Join(filepath.Dir(src.Abs), p)
	if strings.HasPrefix(p, string(filepath.Separator)) {
		abs = filepath.Join(currentRoot, p)
	}
	if !strings.EqualFold(alias, "self") || o.RepoRoot == "" || o.Loader == nil {
		return abs
	}
	if _, err := o.Loader.Load(abs); err == nil || !errors.Is(err, fs.ErrNotExist) {
		return abs
	}
	rooted := filepath.Join(o.RepoRoot, p)
	if _, err := o.Loader.Load(rooted); err == nil {
		return rooted
	}
	return abs
}

func (e *expander) checkLimits(abs string, at *yaml.Node, f *frame) bool {
	if f.depth+1 > MaxDepth {
		e.emit(RuleLimits, diag.Error, e.at(at, f), fmt.Sprintf("templates are nested more than %d levels deep", MaxDepth))
		return false
	}
	e.files[abs] = true
	if len(e.files) > MaxFiles {
		if !e.limitShown {
			e.limitShown = true
			e.emit(RuleLimits, diag.Error, e.at(at, f), fmt.Sprintf("more than %d template files are included", MaxFiles))
		}
		return false
	}
	return true
}

// checkCycle compares bound parameters, not just paths: guarded recursion can terminate.
func (e *expander) checkCycle(tf *frame, at *yaml.Node, f *frame) bool {
	params, _ := tf.ctx.Lookup("parameters")
	state, known := params.JSON()
	known = known && knownParameterState(params)
	for prev := tf.parent; prev != nil; prev = prev.parent {
		p, _ := prev.ctx.Lookup("parameters")
		previous, ok := p.JSON()
		if prev.src.Abs != tf.src.Abs || prev.repoRoot != tf.repoRoot {
			continue
		}
		if !known && sameParameterState(params, p) {
			e.emit(RuleUnresolved, diag.Info, e.at(at, f), fmt.Sprintf("recursive template %q has unknown parameters; further expansion cannot be resolved statically", tf.src.Path))
			return false
		}
		if !known || !ok || previous != state {
			continue
		}
		var names []string
		for _, path := range tf.stack[prev.depth:] {
			names = append(names, e.opts.Loader.Display(path))
		}
		e.emit(RuleLimits, diag.Error, e.at(at, f), "template include cycle: "+strings.Join(names, " -> "))
		return false
	}
	return true
}

func sameParameterState(a, b expr.Value) bool {
	if a.Kind != b.Kind || a.B != b.B || a.N != b.N || a.S != b.S || len(a.Arr) != len(b.Arr) || len(a.Obj) != len(b.Obj) {
		return false
	}
	for i := range a.Arr {
		if !sameParameterState(a.Arr[i], b.Arr[i]) {
			return false
		}
	}
	for i := range a.Obj {
		if !strings.EqualFold(a.Obj[i].Key, b.Obj[i].Key) || !sameParameterState(a.Obj[i].Val, b.Obj[i].Val) {
			return false
		}
	}
	return true
}

func knownParameterState(v expr.Value) bool {
	if !v.Known() || expr.HasDynamic(v.S) {
		return false
	}
	for _, item := range v.Arr {
		if !knownParameterState(item) {
			return false
		}
	}
	for _, kv := range v.Obj {
		if expr.HasDynamic(kv.Key) || !knownParameterState(kv.Val) {
			return false
		}
	}
	return true
}

// include expands a "- template:" sequence item and returns the resulting items.
func (e *expander) include(item *yaml.Node, f *frame, key string) ([]*yaml.Node, bool) {
	if e.exhausted {
		return nil, false
	}
	_, tv := yamlx.Get(item, "template")
	tpl, tf, ok := e.resolveRef(tv, f)
	if !ok {
		return nil, false
	}
	var passed *yaml.Node
	if _, pv := yamlx.Get(item, "parameters"); pv != nil {
		passed = e.node(pv, f, "parameters")
	}
	tf.ctx.Set("parameters", e.bind(tpl, passed, tv, f, tf))
	if !e.checkCycle(tf, tv, f) {
		return nil, false
	}
	bodyKey, body := templateBody(tpl.Root, key)
	if body == nil {
		if bodyKey != "" {
			e.emit(RuleTemplateRef, diag.Error, e.at(tv, f), fmt.Sprintf("template %q defines %s, but is referenced from a %s list", tpl.Path, bodyKey, key))
		}
		return nil, bodyKey == ""
	}
	expanded := e.node(body, tf, bodyKey)
	if bodyKey == "variables" && yamlx.IsMap(expanded) {
		expanded = e.varsToList(expanded, tf)
	}
	if !yamlx.IsSeq(expanded) {
		return nil, false
	}
	return expanded.Content, !e.doc.Open(expanded)
}

// templateBody returns the body key and node of a template for a reference from a key list.
// If the template has a different body, the returned node is nil and bodyKey names it.
func templateBody(root *yaml.Node, key string) (string, *yaml.Node) {
	if isBodyKey(key) {
		if v := yamlx.Value(root, key); v != nil {
			return key, v
		}
	}
	for _, k := range bodyKeys {
		if v := yamlx.Value(root, k); v != nil {
			if isBodyKey(key) {
				return k, nil
			}
			return k, v
		}
	}
	return "", nil
}

// varsToList converts an expanded variables mapping to the equivalent list form.
func (e *expander) varsToList(m *yaml.Node, f *frame) *yaml.Node {
	out := e.newNode(yaml.SequenceNode, "!!seq", m, f)
	out.Style = 0
	yamlx.Pairs(m, func(k, v *yaml.Node) {
		item := e.newNode(yaml.MappingNode, "!!map", k, f)
		nk, vk := e.newNode(yaml.ScalarNode, "!!str", k, f), e.newNode(yaml.ScalarNode, "!!str", v, f)
		item.Style, nk.Value, nk.Style, vk.Value, vk.Style = 0, "name", 0, "value", 0
		item.Content = []*yaml.Node{nk, k, vk, v}
		out.Content = append(out.Content, item)
	})
	return out
}

// bind validates passed parameters against the template's declarations and returns the parameters object.
// Templates that declare parameters in the legacy map form (or not at all) accept any parameter.
func (e *expander) bind(tpl *yamlx.Source, passed, ref *yaml.Node, f, tf *frame) expr.Value {
	decl := DeclaredParams(tpl.Root)
	strict := StrictParams(decl)
	var extra []expr.KV
	yamlx.Pairs(passed, func(k, v *yaml.Node) {
		if expr.HasTemplate(k.Value) || FindParam(decl, k.Value) != nil {
			return
		}
		if strict {
			e.emit(RuleUnexpectedParam, diag.Error, e.at(k, f), fmt.Sprintf("template %q does not declare parameter %q", tpl.Path, k.Value))
			return
		}
		extra = append(extra, expr.KV{Key: k.Value, Val: expr.FromNode(v)})
	})
	var kvs []expr.KV
	for _, p := range decl {
		kvs = append(kvs, expr.KV{Key: p.Name, Val: e.bindOne(tpl, p, passed, ref, f, tf)})
	}
	return paramsObject(append(kvs, extra...))
}

func (e *expander) bindOne(tpl *yamlx.Source, p *Param, passed, ref *yaml.Node, f, tf *frame) expr.Value {
	if _, pv := yamlx.GetFold(passed, p.Name); pv != nil {
		v, prob := typed(p, pv)
		if prob != "" {
			e.emit(RuleParamValue, diag.Error, e.at(pv, f), fmt.Sprintf("parameter %q of template %q %s", p.Name, tpl.Path, prob))
		}
		return v
	}
	if yamlx.IsMap(passed) && e.doc.Open(passed) {
		return expr.Unknown()
	}
	if p.Default == nil {
		e.emit(RuleMissingParam, diag.Error, e.at(ref, f), fmt.Sprintf("template %q requires parameter %q (it has no default)", tpl.Path, p.Name))
		return expr.Unknown()
	}
	v, prob := typed(p, e.node(p.Default, tf, p.Name))
	if prob != "" {
		e.emit(RuleParamValue, diag.Error, e.at(p.Default, tf), fmt.Sprintf("default of parameter %q %s", p.Name, prob))
	}
	return v
}

// extends resolves "extends: template" on an expanded root mapping.
func (e *expander) extends(root *yaml.Node, f *frame) *yaml.Node {
	_, ev := yamlx.Get(root, "extends")
	if ev == nil {
		return root
	}
	tv := yamlx.Value(ev, "template")
	if tv == nil {
		return root
	}
	if e.exhausted {
		e.doc.MarkOpen(root)
		return root
	}
	if e.doc.HasOrigin(tv) {
		o := e.doc.OriginOf(tv)
		copy := *f
		copy.src = o.Src
		f = &copy
		tv = o.Node
	}
	tpl, tf, ok := e.resolveRef(tv, f)
	if !ok {
		e.doc.MarkOpen(root)
		return root
	}
	tf.ctx.Set("parameters", e.bind(tpl, yamlx.Value(ev, "parameters"), tv, f, tf))
	e.extendsSources[tpl] = true
	if !e.checkCycle(tf, tv, f) {
		e.doc.MarkOpen(root)
		return root
	}
	body := e.node(tpl.Root, tf, "")
	if !yamlx.IsMap(body) {
		e.doc.MarkOpen(root)
		return root
	}
	body = e.extends(body, tf)
	merged := e.newNode(yaml.MappingNode, "!!map", root, f)
	e.choices[merged] = append(e.choices[merged], e.choices[root]...)
	e.choices[merged] = append(e.choices[merged], e.choices[body]...)
	yamlx.Pairs(root, func(k, v *yaml.Node) {
		if k.Value != "extends" {
			merged.Content = append(merged.Content, k, v)
		}
	})
	yamlx.Pairs(body, func(k, v *yaml.Node) {
		if k.Value != "parameters" && !yamlx.Has(merged, k.Value) {
			merged.Content = append(merged.Content, k, v)
		}
	})
	if e.doc.Open(body) {
		e.doc.MarkOpen(merged)
	}
	return merged
}
