package template

import (
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
