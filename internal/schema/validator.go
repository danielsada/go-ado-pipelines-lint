package schema

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/danielsada/go-ado-pipelines-lint/internal/expr"
	"github.com/danielsada/go-ado-pipelines-lint/internal/yamlx"
)

// ErrorKind classifies validation errors.
type ErrorKind int

// Error kinds.
const (
	UnknownProperty ErrorKind = iota
	MissingRequired
	InvalidValue
	Deprecated
	UnknownTask
	UnknownInput
	MissingInput
	InvalidInput
)

// Rule IDs for each error kind.
var kindRule = map[ErrorKind]string{
	UnknownProperty: "ADO003",
	MissingRequired: "ADO005",
	InvalidValue:    "ADO006",
	Deprecated:      "ADO007",
	UnknownTask:     "ADO501",
	UnknownInput:    "ADO502",
	MissingInput:    "ADO503",
	InvalidInput:    "ADO502",
}

// Error is a schema violation at a node.
type Error struct {
	Kind    ErrorKind
	Node    *yaml.Node
	Message string
}

// RuleID returns the lint rule ID for the error.
func (e Error) RuleID() string { return kindRule[e.Kind] }

// Validator validates YAML trees against the schema.
type Validator struct {
	r *Root
	// Open reports whether a collection may hold unresolved content; required checks are skipped for it.
	Open func(*yaml.Node) bool
	// Conditional reports whether a node came from an unresolved if/else branch. Elements there that
	// match no known shape are not reported: they are often deliberate error traps such as
	// - 'Unsupported configuration': error
	Conditional func(*yaml.Node) bool
	// KnownTasks lists custom references whose inputs are not validated.
	KnownTasks []string
	taskDef    *Schema
	taskCache  map[string]*Schema
}

// NewValidator creates a validator for the embedded schema.
func NewValidator() (*Validator, error) {
	r, err := Load()
	if err != nil {
		return nil, err
	}
	return &Validator{r: r, Open: func(*yaml.Node) bool { return false }, Conditional: func(*yaml.Node) bool { return false }, taskDef: r.Def("task"), taskCache: map[string]*Schema{}}, nil
}

// Root returns the underlying schema.
func (v *Validator) Root() *Root { return v.r }

// Validate validates n against the named definition (e.g. "pipeline", "stepsTemplate").
func (v *Validator) Validate(n *yaml.Node, def string) []Error {
	errs := v.validate(n, v.r.Def(def), "")
	return dedupe(errs)
}

func dedupe(errs []Error) []Error {
	type key struct {
		n   *yaml.Node
		msg string
	}
	seen := map[key]bool{}
	out := errs[:0]
	for _, e := range errs {
		k := key{e.Node, e.Message}
		if !seen[k] {
			seen[k] = true
			out = append(out, e)
		}
	}
	return out
}

func isTemplateScalar(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && expr.HasTemplate(n.Value)
}

func isDynamicScalar(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && expr.HasDynamic(n.Value)
}

func (v *Validator) validate(n *yaml.Node, s *Schema, key string) []Error {
	n = yamlx.Deref(n)
	if s == nil || n == nil || isTemplateScalar(n) {
		return nil
	}
	if s == v.taskDef {
		return v.validateTask(n)
	}
	var errs []Error
	if len(s.AnyOf) > 0 {
		errs = append(errs, v.anyOf(n, s.AnyOf, key)...)
	}
	if len(s.OneOf) > 0 {
		errs = append(errs, v.anyOf(n, s.OneOf, key)...)
	}
	if t := v.r.target(s); t != nil {
		errs = append(errs, v.validate(n, t, key)...)
	}
	return append(errs, v.self(n, s, key)...)
}

func nodeKind(n *yaml.Node) string {
	switch n.Kind {
	case yaml.MappingNode:
		return "object"
	case yaml.SequenceNode:
		return "array"
	}
	return "scalar"
}

func typeAccepts(t, kind string) bool {
	switch t {
	case "":
		return true
	case "object", "array":
		return t == kind
	}
	return kind == "scalar"
}

// accepts reports whether schema s can accept a node of the given kind.
func (v *Validator) accepts(s *Schema, kind string, depth int) bool {
	if s == nil || depth > 16 {
		return true
	}
	if s.Type != "" {
		return typeAccepts(s.Type, kind)
	}
	if len(s.AnyOf) > 0 {
		for _, b := range s.AnyOf {
			if v.accepts(b, kind, depth+1) {
				return true
			}
		}
		return false
	}
	if t := v.r.target(s); t != nil {
		return v.accepts(t, kind, depth+1)
	}
	return true
}

var (
	boolRe = regexp.MustCompile(`(?i)^(true|false|y|yes|n|no|on|off)$`)
	intRe  = regexp.MustCompile(`^-?[0-9]+$`)
)

func (v *Validator) self(n *yaml.Node, s *Schema, key string) []Error {
	if err, ok := checkType(n, s.Type); !ok {
		return []Error{err}
	}
	switch n.Kind {
	case yaml.MappingNode:
		return v.mapping(n, s)
	case yaml.SequenceNode:
		if s.Items == nil {
			return nil
		}
		var errs []Error
		for _, it := range n.Content {
			errs = append(errs, v.validate(it, s.Items, "")...)
		}
		return errs
	}
	return scalarChecks(n, s, key)
}

func checkType(n *yaml.Node, t string) (Error, bool) {
	if t == "" || yamlx.IsNull(n) {
		return Error{}, true
	}
	if !typeAccepts(t, nodeKind(n)) {
		return Error{Kind: InvalidValue, Node: n, Message: fmt.Sprintf("expected %s, found %s", kindLabel(t), yamlx.KindName(n))}, false
	}
	if isDynamicScalar(n) {
		return Error{}, true
	}
	if t == "boolean" && !boolRe.MatchString(n.Value) {
		return Error{Kind: InvalidValue, Node: n, Message: fmt.Sprintf("expected a boolean (true or false), found %q", n.Value)}, false
	}
	if t == "integer" && !intRe.MatchString(strings.TrimSpace(n.Value)) {
		return Error{Kind: InvalidValue, Node: n, Message: fmt.Sprintf("expected an integer, found %q", n.Value)}, false
	}
	return Error{}, true
}

func kindLabel(t string) string {
	switch t {
	case "object":
		return "a mapping"
	case "array":
		return "a sequence"
	case "boolean":
		return "a boolean"
	case "integer":
		return "an integer"
	}
	return "a scalar"
}

func scalarChecks(n *yaml.Node, s *Schema, key string) []Error {
	if isDynamicScalar(n) || yamlx.IsNull(n) {
		return nil
	}
	if s.re != nil && !s.re.MatchString(n.Value) && key != "job" {
		if alts := literalAlternatives(s); len(alts) > 0 {
			return []Error{{Kind: InvalidValue, Node: n, Message: fmt.Sprintf("invalid value %q; expected %s", n.Value, strings.Join(alts, " or "))}}
		}
		return []Error{{Kind: InvalidValue, Node: n, Message: fmt.Sprintf("invalid value %q (must match %s)", n.Value, s.Pattern)}}
	}
	if len(s.Enum) > 0 && !inEnum(n.Value, s.Enum, s.IgnoreCase == "value" || s.IgnoreCase == "all") {
		return []Error{{Kind: InvalidValue, Node: n, Message: fmt.Sprintf("invalid value %q; expected one of: %s", n.Value, strings.Join(s.Enum, ", "))}}
	}
	return nil
}

func inEnum(v string, enum []string, fold bool) bool {
	for _, e := range enum {
		if e == v || (fold && strings.EqualFold(e, v)) {
			return true
		}
	}
	return false
}

// findProp finds the property schema for key, honoring ignoreCase and aliases.
func findProp(s *Schema, key string) (*Schema, string) {
	if p, ok := s.Properties[key]; ok {
		return p, key
	}
	for name, p := range s.Properties {
		fold := p.IgnoreCase == "key" || p.IgnoreCase == "all"
		if fold && strings.EqualFold(name, key) {
			return p, name
		}
		for _, a := range p.Aliases {
			if a == key || (fold && strings.EqualFold(a, key)) {
				return p, name
			}
		}
	}
	return nil, ""
}

func propNames(s *Schema) []string {
	names := make([]string, 0, len(s.Properties))
	for k := range s.Properties {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

func unknownMsg(what, key string, s *Schema, context string) string {
	msg := fmt.Sprintf("unknown %s %q%s", what, key, context)
	if sug := suggest(key, propNames(s)); sug != "" {
		msg += fmt.Sprintf("; did you mean %q?", sug)
	}
	return msg
}

func (v *Validator) mapping(n *yaml.Node, s *Schema) []Error {
	if s.Properties == nil && s.additionalProperties {
		return nil
	}
	var errs []Error
	present := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, val := n.Content[i], n.Content[i+1]
		if expr.HasTemplate(k.Value) {
			continue
		}
		p, name := findProp(s, k.Value)
		if p == nil {
			if !s.additionalProperties {
				errs = append(errs, Error{Kind: UnknownProperty, Node: k, Message: unknownMsg("property", k.Value, s, "")})
			}
			continue
		}
		present[name] = true
		if p.DeprecationMessage != "" {
			errs = append(errs, Error{Kind: Deprecated, Node: k, Message: p.DeprecationMessage})
		}
		if name == "paths" {
			// The schema reuses the branch-name pattern for path filters, which rejects valid
			// paths such as /docs/* or sdk/storage/; only check the shape.
			if !yamlx.IsMap(val) && !yamlx.IsSeq(val) && !yamlx.IsNull(val) && !isTemplateScalar(val) {
				errs = append(errs, Error{Kind: InvalidValue, Node: val, Message: "paths must be a mapping with include/exclude lists"})
			}
			continue
		}
		errs = append(errs, v.validate(val, p, name)...)
	}
	if v.Open(n) {
		return errs
	}
	for _, r := range s.Required {
		if !present[r] {
			errs = append(errs, Error{Kind: MissingRequired, Node: n, Message: fmt.Sprintf("missing required property %q", r)})
		}
	}
	return errs
}

func hasKey(n *yaml.Node, key string) bool {
	k, _ := yamlx.Get(n, key)
	return k != nil
}

// discriminate narrows anyOf candidates for a mapping using firstProperty/required keys.
func (v *Validator) discriminate(n *yaml.Node, cands []*Schema) (matched []*Schema, keys []string) {
	var plain []*Schema
	for _, c := range cands {
		fps := v.r.firstProps(c)
		req := v.r.requiredKeys(c)
		switch {
		case len(fps) > 0:
			keys = append(keys, fps[0])
			if hasKey(n, fps[0]) {
				matched = append(matched, c)
			}
		case len(req) > 0:
			keys = append(keys, req[0])
			if allKeys(n, req) {
				matched = append(matched, c)
			}
		default:
			plain = append(plain, c)
		}
	}
	if len(matched) > 0 {
		return matched, nil
	}
	return plain, uniq(keys)
}

func allKeys(n *yaml.Node, keys []string) bool {
	for _, k := range keys {
		if !hasKey(n, k) {
			return false
		}
	}
	return true
}

func uniq(ss []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func (v *Validator) anyOf(n *yaml.Node, branches []*Schema, key string) []Error {
	kind := nodeKind(n)
	var cands []*Schema
	for _, b := range branches {
		if v.accepts(b, kind, 0) {
			cands = append(cands, b)
		}
	}
	if len(cands) == 0 {
		if yamlx.IsNull(n) {
			return nil
		}
		return []Error{{Kind: InvalidValue, Node: n, Message: fmt.Sprintf("expected %s, found %s", v.expectedKinds(branches), yamlx.KindName(n))}}
	}
	if n.Kind == yaml.MappingNode {
		var keys []string
		cands, keys = v.discriminate(n, cands)
		if len(cands) == 0 {
			if v.Open(n) || v.Conditional(n) {
				return nil
			}
			return []Error{{Kind: MissingRequired, Node: n, Message: "expected a mapping with one of the keys: " + strings.Join(keys, ", ")}}
		}
		cands = v.covering(n, cands)
	}
	if len(cands) == 1 {
		return v.validate(n, cands[0], key)
	}
	var best []Error
	for i, c := range cands {
		errs := v.validate(n, c, key)
		if len(errs) == 0 {
			return nil
		}
		if i == 0 || score(errs) < score(best) {
			best = errs
		}
	}
	if n.Kind == yaml.ScalarNode {
		if alts := v.alternatives(cands); len(alts) > 0 {
			return []Error{{Kind: InvalidValue, Node: n, Message: fmt.Sprintf("invalid value %q; expected one of: %s", n.Value, strings.Join(alts, ", "))}}
		}
	}
	return best
}

// score ranks candidate errors when choosing an anyOf branch: structural problems count fully, while
// findings about task identity or deprecation barely count, so they can't make a wrong shape win.
func score(errs []Error) float64 {
	s := 0.0
	for _, e := range errs {
		switch e.Kind {
		case UnknownTask, Deprecated:
			s += 0.001
		default:
			s++
		}
	}
	return s
}

// covering keeps the object candidates that define every key of n, if any do.
func (v *Validator) covering(n *yaml.Node, cands []*Schema) []*Schema {
	if len(cands) < 2 {
		return cands
	}
	var out []*Schema
	for _, c := range cands {
		props := v.properties(c)
		if props == nil {
			continue
		}
		all := true
		for i := 0; i+1 < len(n.Content) && all; i += 2 {
			k := n.Content[i].Value
			if p, _ := findProp(&Schema{Properties: props}, k); p == nil && !expr.HasTemplate(k) {
				all = false
			}
		}
		if all {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return cands
	}
	return out
}

// properties returns the properties of s, following references.
func (v *Validator) properties(s *Schema) map[string]*Schema {
	for i := 0; s != nil && i < 16; i++ {
		if s.Properties != nil {
			return s.Properties
		}
		s = v.r.target(s)
	}
	return nil
}

func (v *Validator) alternatives(cands []*Schema) []string {
	var out []string
	for _, c := range cands {
		for i := 0; c != nil && i < 8 && c.Ref != "" && c.Pattern == "" && len(c.Enum) == 0; i++ {
			c = v.r.target(c)
		}
		alts := literalAlternatives(c)
		if len(alts) == 0 {
			return nil
		}
		out = append(out, alts...)
	}
	return out
}

func (v *Validator) expectedKinds(branches []*Schema) string {
	var labels []string
	for _, k := range []string{"scalar", "array", "object"} {
		for _, b := range branches {
			if v.accepts(b, k, 0) {
				labels = append(labels, kindLabel(map[string]string{"scalar": "string"}[k]+map[string]string{"array": "array", "object": "object"}[k]))
				break
			}
		}
	}
	return strings.Join(labels, " or ")
}
