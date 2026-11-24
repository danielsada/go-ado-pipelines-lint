package template

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/danielsada/go-ado-pipelines-lint/internal/diag"
	"github.com/danielsada/go-ado-pipelines-lint/internal/yamlx"
)

type memFS map[string]string

func (m memFS) abs(name string) string {
	return filepath.Join(string(filepath.Separator)+"repo", filepath.FromSlash(name))
}

func (m memFS) read(abs string) ([]byte, error) {
	for k, v := range m {
		if m.abs(k) == abs {
			return []byte(v), nil
		}
	}
	return nil, fs.ErrNotExist
}

func (m memFS) display(abs string) string {
	rel, err := filepath.Rel(m.abs(""), abs)
	if err != nil {
		return abs
	}
	return filepath.ToSlash(rel)
}

func expandFS(t *testing.T, files memFS, entry string) *Result {
	t.Helper()
	loader := NewLoader(files.read, files.display)
	src, err := loader.Load(files.abs(entry))
	if err != nil {
		t.Fatal(err)
	}
	if len(src.ParseDiags) > 0 {
		t.Fatalf("parse diags: %v", src.ParseDiags)
	}
	return ExpandPipeline(src, Options{RepoRoot: files.abs(""), Loader: loader})
}

func normalize(t *testing.T, n *yaml.Node) any {
	t.Helper()
	var v any
	if err := n.Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func parseYAML(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := yaml.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func assertExpanded(t *testing.T, r *Result, want string) {
	t.Helper()
	got := normalize(t, r.Doc.Root)
	if !reflect.DeepEqual(got, parseYAML(t, want)) {
		b, _ := yaml.Marshal(r.Doc.Root)
		t.Errorf("expanded:\n%s\nwant:\n%s", b, want)
	}
}

func rules(ds []diag.Diagnostic) []string {
	var out []string
	for _, d := range ds {
		out = append(out, d.RuleID+" "+d.File+": "+d.Message)
	}
	return out
}

func hasRule(ds []diag.Diagnostic, id, sub string) bool {
	for _, d := range ds {
		if d.RuleID == id && strings.Contains(d.Message, sub) {
			return true
		}
	}
	return false
}

func TestStepsTemplate(t *testing.T) {
	r := expandFS(t, memFS{
		"azure-pipelines.yml": `
steps:
- script: echo start
- template: templates/build.yml
  parameters:
    config: Release
    extra:
    - script: echo extra
`,
		"templates/build.yml": `
parameters:
- name: config
  type: string
  default: Debug
  values: [Debug, Release]
- name: extra
  type: stepList
  default: []
- name: test
  type: boolean
  default: true
steps:
- script: build --config ${{ parameters.config }}
- ${{ parameters.extra }}
- ${{ if parameters.test }}:
  - script: test
- ${{ else }}:
  - script: skip
- ${{ each c in split('a,b', ',') }}:
  - script: echo ${{ c }}
`,
	}, "azure-pipelines.yml")
	if len(r.Diags) > 0 {
		t.Fatalf("diags: %v", rules(r.Diags))
	}
	assertExpanded(t, r, `
steps:
- script: echo start
- script: build --config Release
- script: echo extra
- script: test
- script: echo a
- script: echo b
`)
	steps := yamlx.Items(yamlx.Value(r.Doc.Root, "steps"))
	if loc := r.Doc.Loc(steps[1]); loc.File != "templates/build.yml" || loc.Line != 14 {
		t.Errorf("template step location = %+v", loc)
	}
	if loc := r.Doc.Loc(steps[2]); loc.File != "azure-pipelines.yml" || loc.Line != 8 {
		t.Errorf("parameter step location = %+v", loc)
	}
}

func TestUnknownConditionsVisitAllBranches(t *testing.T) {
	r := expandFS(t, memFS{
		"azure-pipelines.yml": `
parameters:
- name: env
  type: string
  default: dev
pool:
  vmImage:
    ${{ if eq(parameters.env, 'prod') }}: windows-latest
    ${{ else }}: ubuntu-latest
steps:
- ${{ if eq(parameters.env, 'prod') }}:
  - script: deploy prod
- ${{ elseif eq(parameters.env, 'dev') }}:
  - script: deploy dev
- ${{ else }}:
  - script: nothing
`,
	}, "azure-pipelines.yml")
	steps := yamlx.Items(yamlx.Value(r.Doc.Root, "steps"))
	if len(steps) != 3 {
		t.Fatalf("expected all 3 branches, got %d", len(steps))
	}
	for _, s := range steps {
		if !r.Doc.Conditional(s) {
			t.Errorf("step at line %d should be conditional", s.Line)
		}
	}
	if img := yamlx.Str(yamlx.Value(r.Doc.Root, "pool"), "vmImage"); img != "windows-latest" {
		t.Errorf("vmImage = %q", img)
	}
}

func TestParameterErrors(t *testing.T) {
	r := expandFS(t, memFS{
		"azure-pipelines.yml": `
steps:
- template: t.yml
  parameters:
    bogus: 1
    flag: maybe
    choice: c
`,
		"t.yml": `
parameters:
- name: required
  type: string
- name: flag
  type: boolean
  default: false
- name: choice
  values: [a, b]
  default: a
steps:
- script: echo
`,
	}, "azure-pipelines.yml")
	for _, w := range [][2]string{
		{RuleUnexpectedParam, `"bogus"`},
		{RuleMissingParam, `"required"`},
		{RuleParamValue, "must be a boolean"},
		{RuleParamValue, "must be one of a, b"},
	} {
		if !hasRule(r.Diags, w[0], w[1]) {
			t.Errorf("missing %s %s; got %v", w[0], w[1], rules(r.Diags))
		}
	}
}

func TestTemplateResolution(t *testing.T) {
	r := expandFS(t, memFS{
		"azure-pipelines.yml": `
stages:
- template: missing.yml
- template: stages.yml@templates
- template: ${{ variables.dynamic }}
- template: /cycle/a.yml
- template: steps.yml
`,
		"cycle/a.yml": "stages:\n- template: b.yml\n",
		"cycle/b.yml": "stages:\n- template: a.yml\n",
		"steps.yml":   "steps:\n- script: echo\n",
	}, "azure-pipelines.yml")
	for _, w := range [][2]string{
		{RuleTemplateRef, "not found"},
		{RuleUnresolved, `repository "templates"`},
		{RuleUnresolved, "cannot be resolved statically"},
		{RuleLimits, "cycle: cycle/a.yml -> cycle/b.yml -> cycle/a.yml"},
		{RuleTemplateRef, "defines steps, but is referenced from a stages list"},
	} {
		if !hasRule(r.Diags, w[0], w[1]) {
			t.Errorf("missing %s %q; got %v", w[0], w[1], rules(r.Diags))
		}
	}
	if !r.Doc.Open(yamlx.Value(r.Doc.Root, "stages")) {
		t.Errorf("stages should be marked open")
	}
}

func TestGuardedRecursion(t *testing.T) {
	r := expandFS(t, memFS{
		"azure-pipelines.yml": "jobs:\n- template: run-tests.yml\n  parameters:\n    rerunTests: true\n",
		"run-tests.yml": `parameters:
- name: rerunTests
  type: boolean
  default: false
- name: attempt
  type: number
  default: 0
jobs:
- job: tests_${{ parameters.attempt }}
  steps:
  - script: echo run
- ${{ if eq(parameters.rerunTests, true) }}:
  - template: run-tests.yml
    parameters:
      attempt: 1
`,
	}, "azure-pipelines.yml")
	if len(r.Diags) != 0 {
		t.Fatalf("guarded recursion: %v", rules(r.Diags))
	}
	jobs := yamlx.Items(yamlx.Value(r.Doc.Root, "jobs"))
	if len(jobs) != 2 || yamlx.Str(jobs[0], "job") != "tests_0" || yamlx.Str(jobs[1], "job") != "tests_1" {
		t.Fatalf("unexpected jobs: %v", normalize(t, r.Doc.Root))
	}
}

func TestRecursiveParameterStates(t *testing.T) {
	for _, value := range []string{"${{ parameters.flag }}", "${{ not(parameters.flag) }}"} {
		r := expandFS(t, memFS{
			"azure-pipelines.yml": "steps:\n- template: recurse.yml\n",
			"recurse.yml":         "parameters:\n- name: flag\n  type: boolean\n  default: false\nsteps:\n- template: recurse.yml\n  parameters:\n    flag: " + value + "\n",
		}, "azure-pipelines.yml")
		if !hasRule(r.Diags, RuleLimits, "cycle") {
			t.Errorf("%s: expected a cycle, got %v", value, rules(r.Diags))
		}
	}
}

func TestUnknownRecursionIsPartial(t *testing.T) {
	r := expandFS(t, memFS{
		"azure-pipelines.yml": "parameters:\n- name: flag\n  type: boolean\nsteps:\n- template: recurse.yml\n  parameters:\n    flag: ${{ parameters.flag }}\n",
		"recurse.yml":         "parameters:\n- name: flag\n  type: boolean\nsteps:\n- ${{ if parameters.flag }}:\n  - template: recurse.yml\n    parameters:\n      flag: ${{ parameters.flag }}\n",
	}, "azure-pipelines.yml")
	if hasRule(r.Diags, RuleLimits, "") || !hasRule(r.Diags, RuleUnresolved, "unknown parameters") || len(r.Diags) != 1 {
		t.Errorf("unknown recursion should stop as unresolved, not as an error: %v", rules(r.Diags))
	}
}

func TestParameterizedRecursionDepthLimit(t *testing.T) {
	for _, length := range []int{MaxDepth - 1, MaxDepth} {
		r := expandFS(t, memFS{
			"azure-pipelines.yml": "steps:\n- template: recurse.yml\n",
			"recurse.yml": fmt.Sprintf(`parameters:
- name: path
  type: string
  default: ''
steps:
- bash: echo run
- ${{ if lt(length(parameters.path), %d) }}:
  - template: recurse.yml
    parameters:
      path: ${{ format('{0}x', parameters.path) }}
`, length),
		}, "azure-pipelines.yml")
		if got := hasRule(r.Diags, RuleLimits, "levels deep"); got != (length == MaxDepth) {
			t.Errorf("length %d: unexpected limit diagnostics: %v", length, rules(r.Diags))
		}
		if hasRule(r.Diags, RuleLimits, "cycle") || len(yamlx.Items(yamlx.Value(r.Doc.Root, "steps"))) != MaxDepth {
			t.Errorf("length %d: expected %d expanded steps and no cycle: %v", length, MaxDepth, rules(r.Diags))
		}
	}
}

func TestConditionalMappingBranchIntegrity(t *testing.T) {
	r := expandFS(t, memFS{
		"main.yml": `parameters:
- name: inline
  type: boolean
  default: false
- name: extra
  type: object
steps:
- template: branches.yml
  parameters:
    inline: ${{ parameters.inline }}
    extra: ${{ parameters.extra }}
`,
		"branches.yml": `parameters:
- name: inline
  type: boolean
- name: extra
  type: object
steps:
- task: Bash@3
  inputs:
    workingDirectory: src
    ${{ insert }}: ${{ parameters.extra }}
    ${{ if parameters.inline }}:
      targetType: inline
      script: echo inline
    ${{ else }}:
      targetType: filePath
      filePath: run.sh
`,
	}, "main.yml")
	if len(r.Diags) != 0 {
		t.Fatal(rules(r.Diags))
	}
	seen := map[string]bool{}
	for _, doc := range r.Alternatives {
		normalize(t, doc.Root)
		inputs := yamlx.Value(yamlx.Items(yamlx.Value(doc.Root, "steps"))[0], "inputs")
		if !doc.Open(inputs) || yamlx.Value(inputs, "workingDirectory").Value != "src" {
			t.Fatal("alternate view lost the unresolved insertion or shared inputs")
		}
		kind := yamlx.Value(inputs, "targetType").Value
		key, other, value, line, column := "script", "filePath", "echo inline", 13, 15
		if kind == "filePath" {
			key, other, value, line, column = "filePath", "script", "run.sh", 16, 17
		} else if kind != "inline" {
			t.Fatalf("unexpected targetType %q", kind)
		}
		node := yamlx.Value(inputs, key)
		if node == nil || node.Value != value || yamlx.Has(inputs, other) {
			t.Fatalf("%s branch contains missing or mixed inputs", kind)
		}
		loc := doc.Loc(node)
		if loc.File != "branches.yml" || loc.Line != line || loc.Column != column || !doc.Conditional(node) {
			t.Errorf("%s branch lost origin or conditional metadata: %+v", kind, loc)
		}
		seen[kind] = true
	}
	if !seen["inline"] || !seen["filePath"] {
		t.Errorf("missing branch views: %v", seen)
	}
}

func TestIndependentConditionalMappings(t *testing.T) {
	const choices = 8
	var source strings.Builder
	source.WriteString("parameters:\n")
	for i := 0; i < choices; i++ {
		fmt.Fprintf(&source, "- name: flag%d\n  type: boolean\n  default: false\n", i)
	}
	source.WriteString("steps:\n- bash: echo run\n  env:\n")
	for i := 0; i < choices; i++ {
		fmt.Fprintf(&source, "    ${{ if parameters.flag%d }}:\n      FLAG_%d: first\n    ${{ else }}:\n      FLAG_%d: second\n", i, i, i)
	}
	r := expandFS(t, memFS{"main.yml": source.String()}, "main.yml")
	if len(r.Diags) != 0 || len(r.Alternatives) >= 1<<choices {
		t.Fatalf("independent choices should not create a Cartesian product: views=%d, diagnostics=%v", len(r.Alternatives), rules(r.Diags))
	}
	seen := map[string]bool{}
	for _, doc := range r.Alternatives {
		normalize(t, doc.Root)
		env := yamlx.Value(yamlx.Items(yamlx.Value(doc.Root, "steps"))[0], "env")
		if len(env.Content) != choices*2 {
			t.Fatal("an alternate view lost unrelated environment variables")
		}
		changed := 0
		yamlx.Pairs(env, func(k, v *yaml.Node) {
			switch v.Value {
			case "second":
				seen[k.Value] = true
				changed++
			case "first":
			default:
				t.Errorf("unexpected branch value %q", v.Value)
			}
		})
		if changed > 1 {
			t.Fatal("unrelated choices were combined in a single view")
		}
	}
	if len(seen) != choices {
		t.Errorf("only %d of %d alternate branches were checked", len(seen), choices)
	}
}

func TestConditionalAlternativeBudget(t *testing.T) {
	step := `- bash: echo run
  ${{ if parameters.flag }}:
    continueOnError: true
  ${{ else }}:
    continueOnError: false
`
	files := memFS{"main.yml": "parameters:\n- name: flag\n  type: boolean\n  default: false\nsteps:\n" + strings.Repeat(step, 100)}
	res := expandFS(t, files, "main.yml")
	if len(res.Diags) != 1 || res.Diags[0].RuleID != RuleLimits || !strings.Contains(res.Diags[0].Message, "work limit") {
		t.Fatalf("expected one work-limit diagnostic, got %+v", res.Diags)
	}
	if res.Doc.Root != nil || len(res.Alternatives) != 0 {
		t.Fatal("aborted expansion must discard all partial views")
	}
}

func TestExpansionBudgets(t *testing.T) {
	cases := []struct {
		name, parameter, next, branches, limit, want string
	}{
		{"branching recursion", "path", "format('{0}x', parameters.path)", "- template: recurse.yml\n  parameters:\n    path: ${{ format('{0}x', parameters.path) }}\n", "25", "work limit"},
		{"growing scalar", "text", "format('{0}{0}', parameters.text)", "", "1000000000", "text limit"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			calls := fmt.Sprintf("- template: recurse.yml\n  parameters:\n    %s: ${{ %s }}\n%s", tt.parameter, tt.next, tt.branches)
			recurse := fmt.Sprintf("parameters:\n- name: %s\n  type: string\n  default: x\nsteps:\n- bash: echo run\n- ${{ if lt(length(parameters.%s), %s) }}:\n  %s", tt.parameter, tt.parameter, tt.limit, strings.ReplaceAll(calls, "\n", "\n  "))
			r := expandFS(t, memFS{"azure-pipelines.yml": "steps:\n- template: recurse.yml\n", "recurse.yml": recurse}, "azure-pipelines.yml")
			if len(r.Diags) != 1 || !hasRule(r.Diags, RuleLimits, tt.want) || r.Doc.Root != nil || len(r.Alternatives) != 0 {
				t.Fatalf("expansion should abort once with %s, got %v", tt.want, rules(r.Diags))
			}
		})
	}
}

func TestRepositoryAliases(t *testing.T) {
	files := memFS{
		"azure-pipelines.yml":    "steps:\n- template: sub/start.yml@infra\n  parameters:\n    message: hello\n",
		"local.yml":              "steps:\n- bash: echo self\n",
		"infra/sub/start.yml":    "parameters:\n- name: message\n  type: string\nsteps:\n- bash: echo ${{ parameters.message }}\n- template: relative.yml\n- template: /root.yml\n- template: local.yml@self\n",
		"infra/sub/relative.yml": "steps:\n- bash: echo relative\n",
		"infra/root.yml":         "steps:\n- bash: echo rooted\n",
	}
	loader := NewLoader(files.read, files.display)
	src, err := loader.Load(files.abs("azure-pipelines.yml"))
	if err != nil {
		t.Fatal(err)
	}
	r := ExpandPipeline(src, Options{RepoRoot: files.abs(""), Repositories: map[string]string{"infra": files.abs("infra")}, Loader: loader})
	if len(r.Diags) != 0 {
		t.Fatalf("alias expansion: %v", rules(r.Diags))
	}
	assertExpanded(t, r, "steps:\n- bash: echo hello\n- bash: echo relative\n- bash: echo rooted\n- bash: echo self\n")
	steps := yamlx.Items(yamlx.Value(r.Doc.Root, "steps"))
	if loc := r.Doc.Loc(steps[0]); loc.File != "infra/sub/start.yml" || loc.Line != 5 {
		t.Errorf("alias source location: %+v", loc)
	}
	for _, ref := range []string{"missing.yml@infra", "/missing.yml@INFRA"} {
		s, _ := yamlx.Parse("bad.yml", files.abs("bad.yml"), []byte("steps:\n- template: "+ref+"\n"))
		bad := ExpandPipeline(s, Options{RepoRoot: files.abs(""), Repositories: map[string]string{"infra": files.abs("infra")}, Loader: loader})
		if !hasRule(bad.Diags, RuleTemplateRef, "not found") || hasRule(bad.Diags, RuleUnresolved, "") {
			t.Errorf("%s: expected missing mapped template, got %v", ref, rules(bad.Diags))
		}
	}
}

func TestIgnoredRepositoryReferences(t *testing.T) {
	for _, body := range []string{
		"steps:\n- template: bad.yml@INFRA\n- template: missing.yml@unmapped\n",
		"steps:\n- template: ${{ parameters.file }}@infra\n",
		"extends:\n  template: bad.yml@infra\n",
	} {
		files := memFS{"azure-pipelines.yml": body}
		entry := files.abs("azure-pipelines.yml")
		loader := NewLoader(func(abs string) ([]byte, error) {
			if abs != entry {
				t.Errorf("ignored repository should not be read: %s", abs)
			}
			return files.read(abs)
		}, files.display)
		src, err := loader.Load(entry)
		if err != nil {
			t.Fatal(err)
		}
		r := ExpandPipeline(src, Options{
			RepoRoot: files.abs(""), Loader: loader,
			Repositories:       map[string]string{"infra": files.abs("infra")},
			IgnoreRepositories: []string{"Infra", "Unmapped"},
		})
		if len(r.Diags) != 0 || len(r.Doc.Sources) != 1 {
			t.Errorf("ignored references should stay unresolved without diagnostics: %v", rules(r.Diags))
		}
		n := yamlx.Value(r.Doc.Root, "steps")
		if n == nil {
			n = r.Doc.Root
		}
		if !r.Doc.Open(n) {
			t.Error("ignored template content should remain open")
		}
	}
}

// TestUntypedScalarsAreStrings checks that, as in Azure Pipelines, YAML scalars inside object and
// untyped parameters are strings ("false" is truthy), while typed boolean parameters are booleans.
func TestUntypedScalarsAreStrings(t *testing.T) {
	r := expandFS(t, memFS{
		"azure-pipelines.yml": `
parameters:
- name: cfg
  type: object
  default: { publish: false, retries: 9, label: 'false' }
trigger: none
steps:
- ${{ if parameters.cfg.publish }}:
  - script: publish
- ${{ else }}:
  - script: skip
- ${{ if gt(parameters.cfg.retries, 10) }}:
  - script: many
- ${{ if parameters.cfg.label }}:
  - script: quoted string is truthy
- template: t.yml
  parameters:
    name: true
`,
		"t.yml": "parameters:\n- name: name\n  type: string\nsteps:\n- ${{ if eq(parameters.name, 'true') }}:\n  - script: string param\n",
	}, "azure-pipelines.yml")
	assertExpanded(t, r, `
trigger: none
parameters:
- name: cfg
  type: object
  default: { publish: false, retries: 9, label: 'false' }
steps:
- script: publish
- script: many
- script: quoted string is truthy
- script: string param
`)
}

func TestLegacyParamsSelfAndData(t *testing.T) {
	r := expandFS(t, memFS{
		"pipelines/azure-pipelines.yml": `
trigger: none
steps:
- template: legacy.yml
  parameters:
    platform: linux
    shell: unix
- template: pipelines/shared.yml@self
- template: shared.yml@self
`,
		"pipelines/legacy.yml": "parameters:\n  platform: none\nsteps:\n- ${{ if eq(parameters.shell, 'unix') }}:\n  - script: echo ${{ parameters.platform }}\n",
		"pipelines/shared.yml": `
parameters:
- name: matrix
  type: object
  default: [{name: a, template: cpp-app}]
steps:
- ${{ each m in parameters.matrix }}:
  - script: echo ${{ m.template }} $[ format('{0}', '${{ m.name }}') ]
`,
	}, "pipelines/azure-pipelines.yml")
	if len(r.Diags) > 0 {
		t.Fatalf("diags: %v", rules(r.Diags))
	}
	assertExpanded(t, r, `
trigger: none
steps:
- script: echo linux
- script: echo cpp-app $[ format('{0}', 'a') ]
- script: echo cpp-app $[ format('{0}', 'a') ]
`)
}

func TestExtendsAndVariables(t *testing.T) {
	r := expandFS(t, memFS{
		"azure-pipelines.yml": `
trigger: none
variables:
- template: vars.yml
extends:
  template: base.yml
  parameters:
    buildSteps:
    - script: echo user
`,
		"vars.yml": "variables:\n  a: one\n  ${{ if true }}:\n    b: two\n",
		"base.yml": `
parameters:
- name: buildSteps
  type: stepList
  default: []
stages:
- stage: Build
  jobs:
  - job: B
    steps:
    - ${{ each s in parameters.buildSteps }}:
      - ${{ s }}
    - ${{ insert }}: ${{ parameters.missing }}
`,
	}, "azure-pipelines.yml")
	assertExpanded(t, r, `
trigger: none
variables:
- name: a
  value: one
- name: b
  value: two
stages:
- stage: Build
  jobs:
  - job: B
    steps:
    - script: echo user
`)
}
