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

// FindTask returns the schema branch for a built-in task reference like "Bash@3" (case-insensitive).
func (v *Validator) FindTask(ref string) *Schema {
	ref = MajorTaskRef(ref)
	lk := strings.ToLower(ref)
	if b, ok := v.taskCache[lk]; ok {
		return b
	}
	var found *Schema
	for _, b := range v.taskDef.AnyOf {
		if ts := b.Properties["task"]; ts != nil && ts.re != nil && ts.re.MatchString(ref) {
			found = b
			break
		}
	}
	v.taskCache[lk] = found
	return found
}

// TaskVersions returns the known versions of a task name (without the @version).
func (v *Validator) TaskVersions(name string) []string {
	var out []string
	prefix := strings.ToLower(name) + "@"
	for _, b := range v.taskDef.AnyOf {
		ts := b.Properties["task"]
		if ts == nil {
			continue
		}
		ref := strings.TrimSuffix(strings.TrimPrefix(ts.Pattern, "^"), "$")
		if strings.HasPrefix(strings.ToLower(ref), prefix) {
			out = append(out, ref[len(prefix):])
		}
	}
	for _, ref := range v.taskNames() {
		if strings.HasPrefix(strings.ToLower(ref), prefix) {
			out = append(out, ref[len(prefix):])
		}
	}
	sort.Strings(out)
	return uniq(out)
}

// taskNames returns the task references listed in the schema's task-name enum. A few tasks
// (e.g. UseNode@1) appear only there, without an input definition.
func (v *Validator) taskNames() []string {
	var out []string
	if ts := v.taskDef.Properties["task"]; ts != nil {
		for _, b := range ts.AnyOf {
			out = append(out, b.Enum...)
		}
	}
	return out
}

// knownTaskName reports whether ref is in the schema's task-name enum.
func (v *Validator) knownTaskName(ref string) bool {
	ref = MajorTaskRef(ref)
	for _, n := range v.taskNames() {
		if strings.EqualFold(n, ref) {
			return true
		}
	}
	return false
}

var taskGUIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}@`)
var pinnedTaskRe = regexp.MustCompile(`^(.+@[0-9]+)\.[0-9]+\.[0-9]+$`)

// MajorTaskRef normalizes a full version pin to the corresponding major reference.
func MajorTaskRef(ref string) string {
	if m := pinnedTaskRe.FindStringSubmatch(ref); m != nil {
		return m[1]
	}
	return ref
}

func (v *Validator) validateTask(n *yaml.Node) []Error {
	if n.Kind != yaml.MappingNode {
		return []Error{{Kind: InvalidValue, Node: n, Message: "expected a mapping, found " + yamlx.KindName(n)}}
	}
	var errs []Error
	td := v.taskDef
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, val := n.Content[i], n.Content[i+1]
		if expr.HasTemplate(k.Value) {
			continue
		}
		p := td.Properties[k.Value]
		if p == nil {
			p = v.r.Def("taskBase").Properties[k.Value] // e.g. target, which the task definition omits
		}
		if p == nil {
			errs = append(errs, Error{Kind: UnknownProperty, Node: k, Message: unknownMsg("property", k.Value, td, " for a task step")})
			continue
		}
		if k.Value != "task" && k.Value != "inputs" {
			errs = append(errs, v.self(yamlx.Deref(val), p, k.Value)...)
		}
	}
	taskKey, taskNode := yamlx.Get(n, "task")
	if taskKey == nil || !yamlx.IsScalar(taskNode) {
		return errs
	}
	ref := strings.TrimSpace(taskNode.Value)
	if ref == "" || expr.HasDynamic(ref) || !strings.Contains(ref, "@") || taskGUIDRe.MatchString(ref) {
		return errs
	}
	branch := v.FindTask(ref)
	if branch == nil {
		for _, known := range v.KnownTasks {
			if strings.EqualFold(known, ref) {
				return errs
			}
		}
	}
	if branch == nil && v.knownTaskName(ref) {
		return errs
	}
	if branch == nil {
		return append(errs, Error{Kind: UnknownTask, Node: taskNode, Message: v.unknownTaskMsg(ref)})
	}
	if branch.DeprecationMessage != "" {
		errs = append(errs, Error{Kind: Deprecated, Node: taskNode, Message: fmt.Sprintf("task %s is deprecated: %s", ref, branch.DeprecationMessage)})
	}
	return append(errs, v.validateInputs(n, taskNode, branch.Properties["inputs"], ref)...)
}

func (v *Validator) unknownTaskMsg(ref string) string {
	name := ref[:strings.Index(ref, "@")]
	if vers := v.TaskVersions(name); len(vers) > 0 {
		return fmt.Sprintf("%s is not a known version of built-in task %s (known versions: %s)", ref, name, strings.Join(vers, ", "))
	}
	return fmt.Sprintf("%s is not a built-in task (it may be a marketplace or custom task)", ref)
}

func (v *Validator) validateInputs(step, taskNode *yaml.Node, s *Schema, ref string) []Error {
	if s == nil {
		return nil
	}
	inKey, in := yamlx.Get(step, "inputs")
	if in != nil && (isDynamicScalar(in) || yamlx.IsNull(in)) {
		return nil
	}
	if in != nil && in.Kind != yaml.MappingNode {
		return []Error{{Kind: InvalidValue, Node: in, Message: "task inputs must be a mapping, found " + yamlx.KindName(in)}}
	}
	var errs []Error
	present := map[string]bool{}
	yamlx.Pairs(in, func(k, val *yaml.Node) {
		if expr.HasTemplate(k.Value) {
			return
		}
		p, name := findProp(s, k.Value)
		if p == nil {
			if !s.additionalProperties {
				errs = append(errs, Error{Kind: UnknownInput, Node: k, Message: unknownMsg("input", k.Value, s, " for task "+ref)})
			}
			return
		}
		present[name] = true
		if val.Kind == yaml.ScalarNode {
			errs = append(errs, inputValue(val, p, name, ref)...)
		}
	})
	if in != nil && v.Open(in) {
		return errs
	}
	for _, r := range s.Required {
		if !present[r] {
			at := taskNode
			if inKey != nil {
				at = inKey
			}
			errs = append(errs, Error{Kind: MissingInput, Node: at, Message: fmt.Sprintf("task %s is missing required input %q", ref, r)})
		}
	}
	return errs
}

func inputValue(val *yaml.Node, p *Schema, name, ref string) []Error {
	if isDynamicScalar(val) || yamlx.IsNull(val) {
		return nil
	}
	if p.Type == "boolean" && !boolRe.MatchString(val.Value) {
		return []Error{{Kind: InvalidInput, Node: val, Message: fmt.Sprintf("input %q of task %s expects a boolean (true or false), found %q", name, ref, val.Value)}}
	}
	if len(p.Enum) > 0 && !inEnum(val.Value, p.Enum, p.IgnoreCase == "value" || p.IgnoreCase == "all") {
		return []Error{{Kind: InvalidInput, Node: val, Message: fmt.Sprintf("value %q for input %q of task %s is not in the schema's list (%s); the schema may be older than the task", val.Value, name, ref, strings.Join(p.Enum, ", "))}}
	}
	return nil
}
