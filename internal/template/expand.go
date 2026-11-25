package template

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/danielsada/go-ado-pipelines-lint/internal/diag"
	"github.com/danielsada/go-ado-pipelines-lint/internal/expr"
	"github.com/danielsada/go-ado-pipelines-lint/internal/yamlx"
)

// Rule IDs emitted by the expander.
const (
	RuleTemplateRef     = "ADO201"
	RuleUnresolved      = "ADO202"
	RuleUnexpectedParam = "ADO203"
	RuleMissingParam    = "ADO204"
	RuleParamValue      = "ADO205"
	RuleLimits          = "ADO208"
)

// Documented template limits (https://learn.microsoft.com/azure/devops/pipelines/process/templates#template-limits).
const (
	MaxFiles = 100
	MaxDepth = 100
)

// DefaultWorkLimit is the opt-in node-operation budget used when no custom value is given.
const DefaultWorkLimit = 100000

// textBytesPerWork scales the scalar text budget with the work budget (16 MiB per 100,000 operations).
const textBytesPerWork = float64(16<<20) / DefaultWorkLimit

// TextLimit returns the scalar text budget that accompanies a node-operation budget.
func TextLimit(workLimit int) int {
	if workLimit <= 0 {
		return 0
	}
	return int(float64(workLimit) * textBytesPerWork)
}

// Options configures expansion.
type Options struct {
	// RepoRoot resolves template paths that start with "/".
	RepoRoot string
	// Repositories maps lower-case repository aliases to local checkout roots.
	Repositories map[string]string
	// IgnoreRepositories lists aliases skipped without unresolved-reference diagnostics.
	IgnoreRepositories []string
	// SourceRoot is the entry file's checkout root, if different from RepoRoot.
	SourceRoot string
	// Loader loads template files. Required unless NoTemplates is set.
	Loader *Loader
	// NoTemplates disables template file resolution (directives are still evaluated).
	NoTemplates bool
	// WorkLimit optionally bounds node creation and alternative traversal per expansion (0: no limit).
	// Once exceeded, an ADO208 info is reported and no further templates, loops or conditional
	// alternatives are expanded; the partially expanded document is still returned.
	WorkLimit int
	// TextLimit optionally bounds scalar text copied or produced during expansion (0: no limit).
	TextLimit int
}

// Result is the output of expansion.
type Result struct {
	Doc    *yamlx.Doc
	Diags  []diag.Diagnostic
	Params []*Param
	// Alternatives preserves other possible conditional mapping values for linting.
	Alternatives []*yamlx.Doc
	// ExtendsSources identifies files included through extends rather than a body list.
	ExtendsSources map[*yamlx.Source]bool
}

type expander struct {
	opts       Options
	doc        *yamlx.Doc
	diags      []diag.Diagnostic
	files      map[string]bool
	limitShown bool
	cache      map[string]*templateCacheEntry
	// remote records unresolved references already reported, so each is reported once.
	remote         map[string]bool
	work           int
	textBytes      int
	exhausted      bool
	choices        map[*yaml.Node][]*mappingChoice
	extendsSources map[*yamlx.Source]bool
}

type frame struct {
	src      *yamlx.Source
	ctx      *expr.Context
	depth    int
	stack    []string
	cond     bool
	repoRoot string
	parent   *frame
}

func (f *frame) with(name string, v expr.Value) *frame {
	n := *f
	n.ctx = f.ctx.With(name, v)
	return &n
}

func (f *frame) conditional(c bool) *frame {
	if !c || f.cond {
		return f
	}
	n := *f
	n.cond = true
	return &n
}

// ExpandPipeline expands a pipeline entry file. Scalar-typed pipeline parameters
// (string, number, boolean) are treated as unknown since they can be chosen at queue time;
// other parameters use their defaults.
func ExpandPipeline(src *yamlx.Source, opts Options) *Result {
	return expand(src, opts, true)
}

// ExpandTemplate expands a template file on its own, with all parameters unknown.
func ExpandTemplate(src *yamlx.Source, opts Options) *Result {
	return expand(src, opts, false)
}

func expand(src *yamlx.Source, opts Options, pipeline bool) *Result {
	e := &expander{opts: opts, doc: yamlx.NewDoc(src), files: map[string]bool{}, remote: map[string]bool{},
		cache: map[string]*templateCacheEntry{}, choices: map[*yaml.Node][]*mappingChoice{}, extendsSources: map[*yamlx.Source]bool{}}
	res := &Result{Doc: e.doc, ExtendsSources: e.extendsSources}
	defer func() { res.Diags = e.diags }()
	if src.Root == nil {
		return res
	}
	res.Params = DeclaredParams(src.Root)
	f := &frame{src: src, ctx: expr.NewContext(), stack: []string{src.Abs}, repoRoot: opts.RepoRoot}
	if opts.SourceRoot != "" {
		f.repoRoot = opts.SourceRoot
	}
	if !pipeline && !StrictParams(res.Params) {
		// A template without typed declarations accepts any parameter, so on its own nothing is
		// known about its parameters (not even which ones exist).
		f.ctx.Set("parameters", expr.Unknown())
	} else if !pipeline {
		// A template linted on its own has no caller: every parameter value is unknown.
		var kvs []expr.KV
		for _, p := range res.Params {
			kvs = append(kvs, expr.KV{Key: p.Name, Val: expr.Unknown()})
		}
		f.ctx.Set("parameters", paramsObject(kvs))
	} else {
		f.ctx.Set("parameters", e.rootParams(res.Params, f))
	}
	root := e.node(src.Root, f, "")
	if yamlx.IsMap(root) {
		root = e.extends(root, f)
	}
	e.doc.Root = root
	e.forVariants(root, f, func(n *yaml.Node) {
		if n != root {
			e.chargeTree(n, f)
			res.Alternatives = append(res.Alternatives, e.doc.WithRoot(n))
		}
	})
	return res
}

func (e *expander) consume(pos *yaml.Node, f *frame) {
	e.consumeWork(1, pos, f)
}

func (e *expander) consumeWork(amount int, pos *yaml.Node, f *frame) {
	if limit := e.opts.WorkLimit; limit > 0 && !e.exhausted && amount > limit-e.work {
		e.exhaust(pos, f, fmt.Sprintf("work limit of %d node operations", limit))
	}
	e.work += amount
}

func (e *expander) consumeText(value string, pos *yaml.Node, f *frame) {
	if limit := e.opts.TextLimit; limit > 0 && !e.exhausted && len(value) > limit-e.textBytes {
		e.exhaust(pos, f, fmt.Sprintf("text limit of %d bytes", limit))
	}
	e.textBytes += len(value)
}

// exhaust stops further template, loop and alternative expansion. The budget describes adolint's
// own limit rather than a pipeline problem, so it is reported as info and the partial document
// expanded so far is still linted.
func (e *expander) exhaust(pos *yaml.Node, f *frame, limit string) {
	e.exhausted = true
	e.emit(RuleLimits, diag.Info, e.at(pos, f), "template expansion exceeds the opt-in "+limit+
		"; remaining templates, loops and conditional alternatives in this entry are not expanded")
}

func (e *expander) rootParams(ps []*Param, f *frame) expr.Value {
	var kvs []expr.KV
	for _, p := range ps {
		v := expr.Unknown()
		// Scalar parameters can change at queue time (pipelines) or per caller (templates), and
		// untyped scalar defaults are often placeholders, so only list and object defaults are used.
		scalar := (p.Typed && isScalarType(p.Type)) || (!p.Typed && yamlx.IsScalar(p.Default))
		if p.Default != nil && !scalar {
			v, _ = typed(p, e.node(p.Default, f, p.Name))
		}
		kvs = append(kvs, expr.KV{Key: p.Name, Val: v})
	}
	return paramsObject(kvs)
}

func (e *expander) emit(rule string, sev diag.Severity, loc diag.Location, msg string) {
	e.diags = append(e.diags, diag.Diagnostic{RuleID: rule, Severity: sev, Message: msg, Location: loc})
}

// at returns the location of a source node n being expanded in frame f (or of an expanded clone).
func (e *expander) at(n *yaml.Node, f *frame) diag.Location {
	if n == nil {
		return diag.Location{File: f.src.Path, Line: 1, Column: 1}
	}
	if e.doc.HasOrigin(n) {
		return e.doc.Loc(n)
	}
	return diag.Location{File: f.src.Path, Line: n.Line, Column: n.Column}
}

func (e *expander) newNode(kind yaml.Kind, tag string, pos *yaml.Node, f *frame) *yaml.Node {
	e.consume(pos, f)
	e.consumeText(pos.Value, pos, f)
	n := &yaml.Node{Kind: kind, Tag: tag, Line: pos.Line, Column: pos.Column, Style: pos.Style}
	e.doc.SetOrigin(n, f.src, pos)
	if f.cond {
		e.doc.MarkConditional(n)
	}
	return n
}

// clone deep-copies n (a source node or an already expanded node) into the document.
func (e *expander) clone(n *yaml.Node, f *frame) *yaml.Node {
	n = yamlx.Deref(n)
	if n == nil {
		return nil
	}
	c := e.newNode(n.Kind, n.Tag, n, f)
	e.choices[c] = e.choices[n]
	if e.doc.Conditional(n) {
		e.doc.MarkConditional(c)
	}
	c.Value, c.Anchor = n.Value, ""
	c.HeadComment, c.LineComment, c.FootComment = n.HeadComment, n.LineComment, n.FootComment
	for _, ch := range n.Content {
		c.Content = append(c.Content, e.clone(ch, f))
	}
	if e.doc.Open(n) {
		e.doc.MarkOpen(c)
	}
	return c
}

// node expands a source node.
func (e *expander) node(n *yaml.Node, f *frame, key string) *yaml.Node {
	n = yamlx.Deref(n)
	if n == nil {
		return nil
	}
	switch n.Kind {
	case yaml.ScalarNode:
		return e.scalar(n, f)
	case yaml.MappingNode:
		return e.mapping(n, f, key)
	case yaml.SequenceNode:
		return e.seq(n, f, key)
	}
	return e.clone(n, f)
}

func (e *expander) eval(src string, f *frame) expr.Value {
	ast, err := expr.Parse(src)
	if err != nil {
		return expr.Unknown()
	}
	return expr.Eval(ast, f.ctx)
}

func (e *expander) scalar(n *yaml.Node, f *frame) *yaml.Node {
	if !expr.HasTemplate(n.Value) {
		return e.clone(n, f)
	}
	if sp, ok := expr.Whole(n.Value, expr.SpanTemplate); ok {
		if v := e.eval(sp.Expr, f); v.Known() {
			return e.fromValue(v, n, f)
		}
	}
	out := e.clone(n, f)
	out.Value = e.interpolate(n.Value, f)
	e.consumeText(out.Value, n, f)
	return out
}

// interpolate replaces known ${{ }} spans in s with their string values.
func (e *expander) interpolate(s string, f *frame) string {
	spans, _ := expr.Scan(s)
	var b strings.Builder
	last := 0
	for _, sp := range spans {
		if sp.Kind != expr.SpanTemplate {
			continue
		}
		v := e.eval(sp.Expr, f)
		if !v.Known() || v.Kind == expr.KArray || v.Kind == expr.KObject {
			continue
		}
		b.WriteString(s[last:sp.Start])
		b.WriteString(v.String())
		last = sp.End
	}
	b.WriteString(s[last:])
	return b.String()
}

// fromValue converts an evaluated value to a node positioned at pos.
func (e *expander) fromValue(v expr.Value, pos *yaml.Node, f *frame) *yaml.Node {
	if v.Node != nil && (v.Kind == expr.KArray || v.Kind == expr.KObject || v.Kind == expr.KNull ||
		(v.Kind == expr.KString && v.Node.Value == v.S)) {
		return e.clone(v.Node, f)
	}
	switch v.Kind {
	case expr.KArray:
		out := e.newNode(yaml.SequenceNode, "!!seq", pos, f)
		out.Style = 0
		for _, it := range v.Arr {
			out.Content = append(out.Content, e.fromValue(it, pos, f))
		}
		return out
	case expr.KObject:
		out := e.newNode(yaml.MappingNode, "!!map", pos, f)
		out.Style = 0
		for _, kv := range v.Obj {
			k := e.newNode(yaml.ScalarNode, "!!str", pos, f)
			k.Value, k.Style = kv.Key, 0
			out.Content = append(out.Content, k, e.fromValue(kv.Val, pos, f))
		}
		return out
	}
	out := e.newNode(yaml.ScalarNode, "!!str", pos, f)
	out.Value = v.String()
	e.consumeText(out.Value, pos, f)
	if v.Kind == expr.KNull {
		out.Tag = "!!null"
	}
	return out
}

// chain tracks an if/elseif/else sequence.
type chain struct {
	active bool
	state  int
	choice *mappingChoice
}

const (
	chainNone = iota
	chainTaken
	chainUnknown
)

func (c *chain) reset() { c.active, c.choice = false, nil }

// step returns whether the branch is included and whether it is conditional (unresolved).
func (c *chain) step(d *expr.Directive, ok bool, f *frame) (include, cond bool) {
	switch d.Kind {
	case expr.DirIf:
		c.active, c.state = true, chainNone
		c.choice = nil
		return c.branch(d, ok, f)
	case expr.DirElseIf:
		if !c.active {
			c.active, c.state = true, chainNone
		}
		if c.state == chainTaken {
			return false, false
		}
		return c.branch(d, ok, f)
	}
	state := c.state
	if !c.active {
		state = chainUnknown
	}
	c.active = false
	switch state {
	case chainTaken:
		return false, false
	case chainUnknown:
		return true, true
	}
	return true, false
}

func (c *chain) branch(d *expr.Directive, ok bool, f *frame) (include, cond bool) {
	v := expr.Unknown()
	if ok && d.Cond != nil {
		v = expr.Eval(d.Cond, f.ctx)
	}
	b, known := v.Truthy()
	prev := c.state
	switch {
	case !known:
		c.state = chainUnknown
		return true, true
	case b:
		c.state = chainTaken
		return true, prev == chainUnknown
	}
	return false, false
}

func (e *expander) mapping(n *yaml.Node, f *frame, key string) *yaml.Node {
	out := e.newNode(yaml.MappingNode, "!!map", n, f)
	var ch chain
	var scalarRes *yaml.Node
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		var d *expr.Directive
		var err error
		if expr.HasTemplate(k.Value) {
			d, err = expr.ParseDirective(k.Value)
		}
		if d == nil {
			ch.reset()
			e.addPair(out, e.clone(k, f), e.node(v, f, k.Value), f)
			continue
		}
		switch d.Kind {
		case expr.DirIf, expr.DirElseIf, expr.DirElse:
			if inc, cond := ch.step(d, err == nil, f); inc {
				bf := f.conditional(cond)
				b := e.node(v, bf, key)
				e.recordChoice(out, b, &ch, cond)
				e.mergeExpanded(out, b, bf, &scalarRes)
			}
		case expr.DirEach:
			ch.reset()
			e.each(d, err == nil, f, out, func(lf *frame) { e.mergeBody(out, v, lf, key, &scalarRes) })
		case expr.DirInsert:
			ch.reset()
			e.insert(out, v, f, key)
		default:
			ch.reset()
			kn := e.clone(k, f)
			if kv := expr.Eval(d.Expr, f.ctx); err == nil && kv.Known() {
				kn.Value = kv.String()
			} else {
				e.doc.MarkOpen(out)
			}
			e.addPair(out, kn, e.node(v, f, kn.Value), f)
		}
	}
	if len(out.Content) == 0 && scalarRes != nil {
		return scalarRes
	}
	return out
}

func (e *expander) addPair(out, k, v *yaml.Node, f *frame) {
	if v == nil {
		v = e.newNode(yaml.ScalarNode, "!!null", k, f)
	}
	if f.cond {
		if ek, _ := yamlx.Get(out, k.Value); ek != nil {
			return
		}
	}
	out.Content = append(out.Content, k, v)
}

func (e *expander) mergeBody(out, body *yaml.Node, f *frame, key string, scalarRes **yaml.Node) {
	b := e.node(body, f, key)
	e.mergeExpanded(out, b, f, scalarRes)
}

func (e *expander) mergeExpanded(out, b *yaml.Node, f *frame, scalarRes **yaml.Node) {
	switch {
	case yamlx.IsMap(b):
		e.choices[out] = append(e.choices[out], e.choices[b]...)
		for i := 0; i+1 < len(b.Content); i += 2 {
			e.addPair(out, b.Content[i], b.Content[i+1], f)
		}
		if e.doc.Open(b) {
			e.doc.MarkOpen(out)
		}
	case yamlx.IsScalar(b) && *scalarRes == nil:
		*scalarRes = b
	}
}

func (e *expander) insert(out, v *yaml.Node, f *frame, key string) {
	b := e.node(v, f, key)
	if !yamlx.IsMap(b) {
		e.doc.MarkOpen(out)
		return
	}
	for i := 0; i+1 < len(b.Content); i += 2 {
		e.addPair(out, b.Content[i], b.Content[i+1], f)
	}
}

// each calls body once per collection item with the loop variable bound. If the
// collection is unknown, body is called once with an unknown loop variable and open (the
// collection being built, if any) is marked open.
func (e *expander) each(d *expr.Directive, ok bool, f *frame, open *yaml.Node, body func(*frame)) {
	if e.exhausted {
		e.markOpen(open)
		return
	}
	coll := expr.Unknown()
	if ok {
		coll = expr.Eval(d.Coll, f.ctx)
	}
	switch coll.Kind {
	case expr.KUnknown:
		if d.Var != "" {
			body(f.with(d.Var, expr.Unknown()).conditional(true))
		}
		if open != nil {
			e.doc.MarkOpen(open)
		}
	case expr.KArray:
		for _, it := range coll.Arr {
			if e.exhausted {
				e.markOpen(open)
				return
			}
			body(f.with(d.Var, it).conditional(e.isConditional(it)))
		}
	case expr.KObject:
		for _, kv := range coll.Obj {
			if e.exhausted {
				e.markOpen(open)
				return
			}
			item := expr.Value{Kind: expr.KObject, Obj: []expr.KV{{Key: "key", Val: expr.Str(kv.Key)}, {Key: "value", Val: kv.Val}}}
			body(f.with(d.Var, item).conditional(e.isConditional(kv.Val)))
		}
	}
}

func (e *expander) markOpen(n *yaml.Node) {
	if n != nil {
		e.doc.MarkOpen(n)
	}
}

// isConditional reports whether a value came from an unresolved if/else branch.
func (e *expander) isConditional(v expr.Value) bool {
	return v.Node != nil && e.doc.Conditional(v.Node)
}

func singleDirective(item *yaml.Node) (*expr.Directive, *yaml.Node, error) {
	if !yamlx.IsMap(item) || len(item.Content) != 2 || !expr.HasTemplate(item.Content[0].Value) {
		return nil, nil, nil
	}
	d, err := expr.ParseDirective(item.Content[0].Value)
	if d == nil || d.Kind == expr.DirExpr {
		return nil, nil, nil
	}
	return d, item.Content[1], err
}

func (e *expander) seq(n *yaml.Node, f *frame, key string) *yaml.Node {
	out := e.newNode(yaml.SequenceNode, "!!seq", n, f)
	var ch chain
	for _, item := range n.Content {
		item = yamlx.Deref(item)
		if d, body, err := singleDirective(item); d != nil {
			e.seqDirective(out, d, err == nil, body, &ch, f, key)
			continue
		}
		ch.reset()
		e.seqItem(out, item, f, key)
	}
	return out
}

func (e *expander) seqDirective(out *yaml.Node, d *expr.Directive, ok bool, body *yaml.Node, ch *chain, f *frame, key string) {
	switch d.Kind {
	case expr.DirIf, expr.DirElseIf, expr.DirElse:
		if inc, cond := ch.step(d, ok, f); inc {
			e.appendBody(out, body, f.conditional(cond), key)
		}
	case expr.DirEach:
		ch.reset()
		if yamlx.IsMap(body) {
			item := e.newNode(yaml.MappingNode, "!!map", body, f)
			var scalarRes *yaml.Node
			e.each(d, ok, f, item, func(lf *frame) { e.mergeBody(item, body, lf, key, &scalarRes) })
			if len(item.Content) > 0 {
				out.Content = append(out.Content, item)
			} else if e.doc.Open(item) {
				e.doc.MarkOpen(out)
			}
			return
		}
		e.each(d, ok, f, out, func(lf *frame) { e.appendBody(out, body, lf, key) })
	default:
		ch.reset()
		e.appendBody(out, body, f, key)
	}
}

func (e *expander) appendBody(out, body *yaml.Node, f *frame, key string) {
	body = yamlx.Deref(body)
	if body == nil {
		return
	}
	src := body
	if body.Kind != yaml.SequenceNode {
		src = &yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{body}, Line: body.Line, Column: body.Column}
	}
	sub := e.seq(src, f, key)
	out.Content = append(out.Content, sub.Content...)
	if e.doc.Open(sub) {
		e.doc.MarkOpen(out)
	}
}

func (e *expander) seqItem(out, item *yaml.Node, f *frame, key string) {
	if item.Kind == yaml.ScalarNode {
		if sp, ok := expr.Whole(item.Value, expr.SpanTemplate); ok {
			e.seqValue(out, e.eval(sp.Expr, f), item, f)
			return
		}
	}
	if isTemplateRef(item) {
		items, ok := e.include(item, f, key)
		out.Content = append(out.Content, items...)
		if !ok {
			e.doc.MarkOpen(out)
		}
		return
	}
	out.Content = append(out.Content, e.node(item, f, key))
}

// isTemplateRef reports whether a sequence item is a template reference: a mapping with a template
// key and, optionally, parameters. Data objects that merely have a "template" field are not.
func isTemplateRef(item *yaml.Node) bool {
	if !yamlx.IsMap(item) || !yamlx.Has(item, "template") {
		return false
	}
	for i := 0; i+1 < len(item.Content); i += 2 {
		if k := item.Content[i].Value; k != "template" && k != "parameters" {
			return false
		}
	}
	return true
}

func (e *expander) seqValue(out *yaml.Node, v expr.Value, item *yaml.Node, f *frame) {
	switch v.Kind {
	case expr.KUnknown:
		e.doc.MarkOpen(out)
	case expr.KNull:
	case expr.KArray:
		for _, it := range v.Arr {
			out.Content = append(out.Content, e.fromValue(it, item, f))
		}
	default:
		out.Content = append(out.Content, e.fromValue(v, item, f))
	}
}
