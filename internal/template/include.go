package template

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
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
	if ref == "" || expr.HasDynamic(ref) {
		e.emit(RuleUnresolved, diag.Info, e.at(tv, f), fmt.Sprintf("template reference %q cannot be resolved statically; its contents are not linted", tv.Value))
		return nil, nil, false
	}
	path, alias := SplitRef(ref)
	if alias != "" && !strings.EqualFold(alias, "self") {
		e.emit(RuleUnresolved, diag.Info, e.at(tv, f), fmt.Sprintf("template %q is in repository %q; only local templates are expanded, so its contents are not linted", path, alias))
		return nil, nil, false
	}
	abs := e.absPath(path, f)
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
	return src, &frame{src: src, ctx: expr.NewContext(), depth: f.depth + 1, stack: stack, cond: f.cond}, true
}

func (e *expander) absPath(path string, f *frame) string {
	path = filepath.FromSlash(path)
	if strings.HasPrefix(path, string(filepath.Separator)) {
		return filepath.Join(e.opts.RepoRoot, path)
	}
	return filepath.Join(filepath.Dir(f.src.Abs), path)
}

func (e *expander) checkLimits(abs string, at *yaml.Node, f *frame) bool {
	for i, s := range f.stack {
		if s != abs {
			continue
		}
		var names []string
		for _, p := range append(f.stack[i:], abs) {
			names = append(names, e.opts.Loader.Display(p))
		}
		e.emit(RuleLimits, diag.Error, e.at(at, f), "template include cycle: "+strings.Join(names, " -> "))
		return false
	}
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

// include expands a "- template:" sequence item and returns the resulting items.
func (e *expander) include(item *yaml.Node, f *frame, key string) ([]*yaml.Node, bool) {
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
func (e *expander) bind(tpl *yamlx.Source, passed, ref *yaml.Node, f, tf *frame) expr.Value {
	decl := DeclaredParams(tpl.Root)
	yamlx.Pairs(passed, func(k, _ *yaml.Node) {
		if !expr.HasTemplate(k.Value) && FindParam(decl, k.Value) == nil {
			e.emit(RuleUnexpectedParam, diag.Error, e.at(k, f), fmt.Sprintf("template %q does not declare parameter %q", tpl.Path, k.Value))
		}
	})
	var kvs []expr.KV
	for _, p := range decl {
		kvs = append(kvs, expr.KV{Key: p.Name, Val: e.bindOne(tpl, p, passed, ref, f, tf)})
	}
	return paramsObject(kvs)
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
	if e.doc.HasOrigin(tv) {
		o := e.doc.OriginOf(tv)
		f = &frame{src: o.Src, ctx: f.ctx, depth: f.depth, stack: f.stack, cond: f.cond}
		tv = o.Node
	}
	tpl, tf, ok := e.resolveRef(tv, f)
	if !ok {
		e.doc.MarkOpen(root)
		return root
	}
	tf.ctx.Set("parameters", e.bind(tpl, yamlx.Value(ev, "parameters"), tv, f, tf))
	body := e.node(tpl.Root, tf, "")
	if !yamlx.IsMap(body) {
		e.doc.MarkOpen(root)
		return root
	}
	body = e.extends(body, tf)
	merged := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Line: root.Line, Column: root.Column}
	e.doc.SetOrigin(merged, f.src, root)
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
