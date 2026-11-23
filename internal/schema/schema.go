// Package schema validates Azure Pipelines YAML against the official service schema
// (service-schema.json from microsoft/azure-pipelines-vscode, MIT licensed), embedded at build time.
package schema

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

//go:embed service-schema.json
var schemaJSON []byte

// Schema is a (subset of a) JSON schema node as used by service-schema.json.
type Schema struct {
	Ref                  string             `json:"$ref"`
	Type                 string             `json:"type"`
	AnyOf                []*Schema          `json:"anyOf"`
	OneOf                []*Schema          `json:"oneOf"`
	Properties           map[string]*Schema `json:"properties"`
	AdditionalPropsRaw   json.RawMessage    `json:"additionalProperties"`
	Required             []string           `json:"required"`
	FirstProperty        []string           `json:"firstProperty"`
	Pattern              string             `json:"pattern"`
	Enum                 []string           `json:"enum"`
	IgnoreCase           string             `json:"ignoreCase"`
	Aliases              []string           `json:"aliases"`
	Items                *Schema            `json:"items"`
	DeprecationMessage   string             `json:"deprecationMessage"`
	Description          string             `json:"description"`
	additionalProperties bool
	re                   *regexp.Regexp
}

// Root is the parsed schema with its definitions.
type Root struct {
	Definitions map[string]*Schema `json:"definitions"`
}

var (
	loadOnce sync.Once
	loaded   *Root
	errLoad  error
)

// Load returns the embedded schema (parsed once).
func Load() (*Root, error) {
	loadOnce.Do(func() {
		var r Root
		if err := json.Unmarshal(schemaJSON, &r); err != nil {
			errLoad = fmt.Errorf("parsing embedded schema: %w", err)
			return
		}
		for _, s := range r.Definitions {
			if err := s.prepare(); err != nil {
				errLoad = err
				return
			}
		}
		loaded = &r
	})
	return loaded, errLoad
}

func (s *Schema) prepare() error {
	if s == nil {
		return nil
	}
	s.additionalProperties = true
	if len(s.AdditionalPropsRaw) > 0 && string(s.AdditionalPropsRaw) == "false" {
		s.additionalProperties = false
	}
	if s.Pattern != "" {
		p := s.Pattern
		if s.IgnoreCase == "value" || s.IgnoreCase == "all" {
			p = "(?i)" + p
		}
		re, err := regexp.Compile(p)
		if err != nil {
			return fmt.Errorf("schema pattern %q: %w", s.Pattern, err)
		}
		s.re = re
	}
	for _, c := range s.AnyOf {
		if err := c.prepare(); err != nil {
			return err
		}
	}
	for _, c := range s.OneOf {
		if err := c.prepare(); err != nil {
			return err
		}
	}
	for _, c := range s.Properties {
		if err := c.prepare(); err != nil {
			return err
		}
	}
	return s.Items.prepare()
}

// Def returns a named definition.
func (r *Root) Def(name string) *Schema { return r.Definitions[name] }

// target returns the definition referenced by s.Ref, or nil.
func (r *Root) target(s *Schema) *Schema {
	if s == nil || s.Ref == "" {
		return nil
	}
	return r.Definitions[strings.TrimPrefix(s.Ref, "#/definitions/")]
}

// firstProps returns the firstProperty list of s, following references.
func (r *Root) firstProps(s *Schema) []string {
	for i := 0; s != nil && i < 16; i++ {
		if len(s.FirstProperty) > 0 {
			return s.FirstProperty
		}
		s = r.target(s)
	}
	return nil
}

// requiredKeys returns the required list of s, following references.
func (r *Root) requiredKeys(s *Schema) []string {
	for i := 0; s != nil && i < 16; i++ {
		if len(s.Required) > 0 {
			return s.Required
		}
		s = r.target(s)
	}
	return nil
}

// TaskInfo describes a built-in task version known to the schema.
type TaskInfo struct {
	Name        string
	Description string
	Deprecated  string
	Inputs      []string
	Required    []string
}

// Tasks returns all built-in tasks known to the schema, sorted by name.
func (r *Root) Tasks() []TaskInfo {
	var out []TaskInfo
	for _, b := range r.Def("task").AnyOf {
		ts := b.Properties["task"]
		if ts == nil {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(ts.Pattern, "^"), "$")
		ti := TaskInfo{Name: name, Description: ts.Description, Deprecated: b.DeprecationMessage}
		if in := b.Properties["inputs"]; in != nil {
			for k := range in.Properties {
				ti.Inputs = append(ti.Inputs, k)
			}
			sort.Strings(ti.Inputs)
			ti.Required = in.Required
		}
		out = append(out, ti)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// literalAlternatives returns the literal values matched by "^value$" patterns or enums.
func literalAlternatives(s *Schema) []string {
	if len(s.Enum) > 0 {
		return s.Enum
	}
	p := s.Pattern
	if strings.HasPrefix(p, "^") && strings.HasSuffix(p, "$") && !strings.ContainsAny(p[1:len(p)-1], `\[]()*+?.|{}`) {
		return []string{p[1 : len(p)-1]}
	}
	return nil
}

// suggest returns the closest candidate to s (edit distance <= 2), or "".
func suggest(s string, candidates []string) string {
	best, bestD := "", 3
	ls := strings.ToLower(s)
	for _, c := range candidates {
		if d := levenshtein(ls, strings.ToLower(c)); d < bestD {
			best, bestD = c, d
		}
	}
	return best
}

func levenshtein(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
