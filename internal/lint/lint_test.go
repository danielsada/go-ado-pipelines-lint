package lint_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/danielsada/go-ado-pipelines-lint/internal/config"
	"github.com/danielsada/go-ado-pipelines-lint/internal/diag"
	"github.com/danielsada/go-ado-pipelines-lint/internal/lint"
	"github.com/danielsada/go-ado-pipelines-lint/internal/rules"
	"github.com/danielsada/go-ado-pipelines-lint/internal/scripts"
)

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func run(t *testing.T, dir string, cfg *config.Config, overrides map[string]diag.Severity, names ...string) []diag.Diagnostic {
	t.Helper()
	if cfg == nil {
		cfg = config.Default(dir)
	}
	eng, err := lint.New(lint.Options{Registry: rules.Registry(), Config: cfg, Overrides: overrides, RepoRoot: dir, NoScripts: true,
		Display: func(p string) string { r, _ := filepath.Rel(dir, p); return filepath.ToSlash(r) }})
	if err != nil {
		t.Fatal(err)
	}
	var inputs []lint.Input
	for _, n := range names {
		inputs = append(inputs, lint.Input{Abs: filepath.Join(dir, filepath.FromSlash(n)), Path: n})
	}
	ds, err := eng.Lint(context.Background(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	return ds
}

func summary(ds []diag.Diagnostic) string {
	var out []string
	for _, d := range ds {
		out = append(out, d.File+":"+strconv.Itoa(d.Line)+":"+d.FullID()+":"+d.Severity.String())
	}
	return strings.Join(out, " ")
}

func TestSuppressions(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"a.yml": `trigger: none
pool:
  vmImage: ubuntu-20.04 # adolint:disable-line ADO404
steps:
# adolint:disable-next-line
- task: Foo
- task: Bar
  continueOnError: true # adolint:disable-line ADO405, ADO105
- task: Baz
`,
		"b.yml": "# adolint:disable-file ADO401\nsteps:\n- task: Qux\n",
	})
	got := summary(run(t, dir, nil, map[string]diag.Severity{"ADO405": diag.Info}, "a.yml", "b.yml"))
	want := "a.yml:7:ADO105:error a.yml:9:ADO105:error b.yml:3:ADO105:error"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestRemoteExtendsParametersAreLinted(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"azure-pipelines.yml": `trigger: none
resources:
  repositories:
  - repository: tpl
    type: git
    name: org/templates
extends:
  template: v1/t.yml@tpl
  parameters:
    pool:
      name: Hosted
      os: windows
    stages:
    - stage: s
      dependsOn: Nope
      jobs:
      - job: j
        pool:
          vmImage: ubuntu-20.04
        steps:
        - task: NoVersion
        - bash: echo
          bogusKey: 1
`,
	})
	got := summary(run(t, dir, nil, map[string]diag.Severity{"ADO202": diag.Info}, "azure-pipelines.yml"))
	if strings.Contains(got, "ADO103") {
		t.Errorf("stages passed to a remote template may depend on stages it adds: %s", got)
	}
	for _, want := range []string{"azure-pipelines.yml:8:ADO202:info",
		"azure-pipelines.yml:19:ADO404:warning", "azure-pipelines.yml:21:ADO105:error", "azure-pipelines.yml:23:ADO003:error"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
}

func TestSeverityPrecedence(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.yml": "steps:\n- script: echo\n  continueOnError: true\n"})
	cfg, err := config.Parse([]byte("rules:\n  ADO401: error\n  ADO405: off\n"), filepath.Join(dir, ".adolint.yaml"), rules.Registry().Has)
	if err != nil {
		t.Fatal(err)
	}
	if got := summary(run(t, dir, cfg, nil, "a.yml")); got != "a.yml:1:ADO401:error" {
		t.Errorf("config: got %s", got)
	}
	got := summary(run(t, dir, cfg, map[string]diag.Severity{"ADO401": diag.Off, "ADO405": diag.Info, "ADO402": diag.Info}, "a.yml"))
	if got != "a.yml:1:ADO402:info a.yml:3:ADO405:info" {
		t.Errorf("overrides: got %s", got)
	}
}

func TestIgnoreAndTemplates(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"azure-pipelines.yml": "trigger: none\nsteps:\n- template: vendor/steps.yml\n- template: templates/own.yml\n",
		"vendor/steps.yml":    "steps:\n- task: NoVersion\n",
		"templates/own.yml":   "parameters:\n- name: x\n  type: string\n  default: a\nsteps:\n- script: echo ${{ parameters.x }}\n",
		"standalone.yml":      "parameters:\n- name: y\n  type: string\n  default: b\nsteps:\n- script: echo ${{ parameters.y }}\n",
	})
	cfg, err := config.Parse([]byte("ignore: ['vendor/**']\n"), filepath.Join(dir, ".adolint.yaml"), nil)
	if err != nil {
		t.Fatal(err)
	}
	// standalone.yml is template-shaped, so it isn't reported for a missing trigger;
	// templates/own.yml is linted through azure-pipelines.yml only.
	if got := summary(run(t, dir, cfg, nil, "azure-pipelines.yml", "templates/own.yml", "standalone.yml")); got != "" {
		t.Errorf("got %s", got)
	}
	if got := summary(run(t, dir, nil, nil, "azure-pipelines.yml")); got != "vendor/steps.yml:2:ADO105:error" {
		t.Errorf("without ignore: got %s", got)
	}
}

func TestLooksLikeTemplate(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"p.yml":                    "steps:\n- script: echo\n",
		"v.yml":                    "variables:\n  a: b\n",
		"ci/steps/update-tool.yml": "steps:\n- script: echo update\n",
		"ci/jobs/build.yml":        "jobs:\n- job: build\n  steps:\n  - script: echo build\n",
		"ci/stages/build.yml":      "stages:\n- stage: build\n  jobs: []\n",
		"ci/phases/update.yml":     "steps:\n- script: echo update\n",
		"ci/steps/pipeline.yml":    "trigger: none\npool:\n  vmImage: ubuntu-latest\nsteps:\n- script: echo pipeline\n",
		// A steps-only wrapper that forwards whatever parameters it is given.
		"eng/templates/wrapper.yml": "steps:\n- template: /core.yml\n  parameters:\n    ${{ each parameter in parameters }}:\n      ${{ parameter.key }}: ${{ parameter.value }}\n",
		"core.yml":                  "parameters:\n- name: required\n  type: string\nsteps:\n- script: echo ${{ parameters.required }}\n",
	})
	if got := summary(run(t, dir, nil, nil, "p.yml", "v.yml", "eng/templates/wrapper.yml", "ci/steps/update-tool.yml", "ci/jobs/build.yml", "ci/stages/build.yml", "ci/phases/update.yml", "ci/steps/pipeline.yml")); got != "p.yml:1:ADO401:warning" {
		t.Errorf("got %s", got)
	}
}

func TestLintReferencedBranches(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"azure-pipelines.yml": "trigger: none\nsteps:\n- template: templates/gate.yml\n",
		"templates/gate.yml": `parameters:
- name: runGate
  type: boolean
  default: false
steps:
- task: NoVersion
- ${{ if parameters.runGate }}:
  - task: Bash@3
    inputs:
      targetType: inline
      script: echo gate
      condition: succeeded()
- template: nested.yml
`,
		"templates/nested.yml": "steps:\n- task: OtherNoVersion\n",
	})
	for _, all := range []bool{false, true} {
		cfg := config.Default(dir)
		cfg.Templates.LintReferenced = all
		ds := run(t, dir, cfg, nil, "azure-pipelines.yml", "templates/gate.yml")
		counts := map[string]int{}
		for _, d := range ds {
			counts[d.RuleID]++
		}
		if (counts["ADO502"] == 1) != all || counts["ADO105"] != 2 || counts["ADO401"] != 0 {
			t.Errorf("lintReferenced=%t: %s", all, summary(ds))
		}
	}
}

func TestMappedCheckoutLinting(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"main/azure-pipelines.yml": "trigger: none\nsteps:\n- template: steps/gate.yml@Infra\n  parameters:\n    run: true\n",
		"infra/steps/gate.yml":     "parameters:\n- name: run\n  type: boolean\n  default: false\nsteps:\n- ${{ if parameters.run }}:\n  - template: /steps/bad.yml\n",
		"infra/steps/bad.yml":      "steps:\n- task: NoVersion\n",
	})
	cfg, err := config.Parse([]byte("repositories:\n  Infra: ../infra\ntemplates:\n  lintReferenced: true\n"), filepath.Join(dir, "main", ".adolint.yaml"), nil)
	if err != nil {
		t.Fatal(err)
	}
	ds := run(t, filepath.Join(dir, "main"), cfg, map[string]diag.Severity{"ADO201": diag.Error, "ADO202": diag.Info}, "azure-pipelines.yml")
	if len(ds) != 1 || ds[0].RuleID != "ADO105" || ds[0].File != "../infra/steps/bad.yml" || ds[0].Line != 2 {
		t.Errorf("mapped checkout findings: %s", summary(ds))
	}
}

func TestIgnoredRepositoryLinting(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"azure-pipelines.yml": "trigger: none\nsteps:\n- template: bad.yml@INFRA\n- template: missing.yml@unmapped\n- template: missing.yml@other\n",
		"infra/bad.yml":       "trigger: none\nsteps:\n- task: NoVersion\n",
	})
	cfg, err := config.Parse([]byte("repositories:\n  infra: infra\ntemplates:\n  ignoreRepositories: [Infra, Unmapped]\n"), filepath.Join(dir, ".adolint.yaml"), nil)
	if err != nil {
		t.Fatal(err)
	}
	overrides := map[string]diag.Severity{"ADO201": diag.Error, "ADO202": diag.Info}
	for _, standalone := range []bool{false, true} {
		cfg.Templates.LintReferenced = standalone
		ds := run(t, dir, cfg, overrides, "azure-pipelines.yml")
		if len(ds) != 1 || ds[0].RuleID != "ADO202" || !strings.Contains(ds[0].Message, `repository "other"`) {
			t.Errorf("lintReferenced=%t: ignored repositories should not be linted or emit ADO202: %+v", standalone, ds)
		}
	}
	cfg.Templates.LintReferenced = false
	ds := run(t, dir, cfg, overrides, "azure-pipelines.yml", "infra/bad.yml")
	if len(ds) != 2 || !strings.Contains(summary(ds), "infra/bad.yml:3:ADO105:error") {
		t.Errorf("ignored aliases should not hide explicitly scanned files: %s", summary(ds))
	}
}

func TestReferenceDiscoveryContexts(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"azure-pipelines.yml": "trigger: none\npr:\n  paths:\n    include: [other.yml]\nsteps:\n- template: steps.yml@infra\n",
		"other.yml":           "trigger: none\npool:\n  vmImage: ubuntu-latest\nsteps:\n- task: OtherNoVersion\n",
		"build.yml":           "trigger: none\nsteps:\n- task: RootNoVersion\n",
		"infra/steps.yml":     "steps:\n- template: build.yml\n",
		"infra/build.yml":     "steps:\n- bash: echo build\n",
	})
	for _, standalone := range []bool{false, true} {
		cfg := config.Default(dir)
		cfg.Repositories = map[string]string{"infra": "infra"}
		cfg.Templates.LintReferenced = standalone
		ds := run(t, dir, cfg, nil, "azure-pipelines.yml", "other.yml", "build.yml", "infra/steps.yml", "infra/build.yml")
		if len(ds) != 2 || ds[0].RuleID != "ADO105" || ds[1].RuleID != "ADO105" || ds[0].File != "build.yml" || ds[1].File != "other.yml" {
			t.Errorf("lintReferenced=%t: independent pipelines must remain entries: %s", standalone, summary(ds))
		}
	}
}

func TestStandaloneExtendsSchemas(t *testing.T) {
	for _, body := range []string{"steps:\n- bash: echo hi\n", "jobs:\n- job: build\n  steps:\n  - bash: echo hi\n"} {
		dir := writeFiles(t, map[string]string{
			"azure-pipelines.yml": "trigger: none\nextends:\n  template: middle.yml\n",
			"middle.yml":          "extends:\n  template: base.yml\n",
			"base.yml":            "variables:\n  message: hi\n" + body,
		})
		cfg := config.Default(dir)
		cfg.Templates.LintReferenced = true
		if ds := run(t, dir, cfg, nil, "azure-pipelines.yml", "middle.yml", "base.yml"); len(ds) != 0 {
			t.Errorf("valid extends templates should use the extends schema: %s", summary(ds))
		}
	}
}

func TestSingleJobPipelineClassification(t *testing.T) {
	for _, property := range []string{
		"strategy:\n  matrix:\n    linux:\n      image: ubuntu-latest\n",
		"container: ubuntu:latest\n",
		"services:\n  redis: redis\n",
		"workspace:\n  clean: all\n",
		"continueOnError: false\n",
		"queue: Default\n",
		"server: true\n",
	} {
		dir := writeFiles(t, map[string]string{"ci/jobs/build.yml": property + "steps:\n- script: echo hi\n"})
		ds := run(t, dir, nil, nil, "ci/jobs/build.yml")
		found := false
		for _, d := range ds {
			if d.RuleID == "ADO003" {
				t.Errorf("pipeline property %q rejected as a template: %+v", property, ds)
			}
			found = found || d.RuleID == "ADO401"
		}
		if !found {
			t.Errorf("pipeline property %q must retain pipeline checks: %+v", property, ds)
		}
	}
}

func TestMissingDependencyMessage(t *testing.T) {
	dir := writeFiles(t, map[string]string{"azure-pipelines.yml": `trigger: none
stages:
- stage: build
  jobs: []
- stage: test
  variables:
    version: $[ stageDependencies.deploy_web.deploy.outputs['s.version'] ]
  jobs: []
`})
	ds := run(t, dir, nil, nil, "azure-pipelines.yml")
	if len(ds) != 1 || ds[0].RuleID != "ADO605" || !strings.Contains(ds[0].Message, `stage "deploy_web", which does not exist`) || strings.Contains(ds[0].Message, "add deploy_web") {
		t.Errorf("missing stage diagnostic: %+v", ds)
	}
}

type recordingRunner struct {
	bash []*scripts.Script
}

func (r *recordingRunner) ShellCheck(_ context.Context, found []*scripts.Script) ([]scripts.Finding, error) {
	r.bash = append(r.bash, found...)
	var findings []scripts.Finding
	for _, s := range found {
		findings = append(findings, scripts.Finding{ScriptID: s.ID, Code: "SC2164", Level: "warning", Line: 1, Column: 1, Message: "check cd"})
	}
	return findings, nil
}

func (*recordingRunner) PSScriptAnalyzer(context.Context, []*scripts.Script) ([]scripts.Finding, error) {
	return nil, nil
}

func TestReferencedScriptCoverage(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"azure-pipelines.yml": "trigger: none\npool:\n  vmImage: ubuntu-latest\nsteps:\n- template: gate.yml\n",
		"gate.yml": `parameters:
- name: runGate
  type: boolean
  default: false
steps:
- bash: cd common
- script: cd inherited
- ${{ if parameters.runGate }}:
  - bash: cd hidden
`,
	})
	cfg := config.Default(dir)
	cfg.Templates.LintReferenced = true
	runner := &recordingRunner{}
	eng, err := lint.New(lint.Options{Registry: rules.Registry(), Config: cfg, RepoRoot: dir, Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	ds, err := eng.Lint(context.Background(), []lint.Input{{Abs: filepath.Join(dir, "azure-pipelines.yml"), Path: "azure-pipelines.yml"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.bash) != 3 || len(ds) != 3 {
		t.Fatalf("expected 3 unique scripts and findings, got %d scripts, %s", len(runner.bash), summary(ds))
	}
	for i, s := range runner.bash {
		if s.ID != i || s.Raw != []string{"cd common", "cd inherited", "cd hidden"}[i] {
			t.Errorf("script %d: %+v", i, s)
		}
	}
}

func TestConditionalMappingScriptCoverage(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"azure-pipelines.yml": "trigger: none\nsteps:\n- template: template.yml\n",
		"template.yml": `parameters:
- name: kind
  type: string
  default: one
steps:
- task: Bash@3
  inputs:
    ${{ if eq(parameters.kind, 'one') }}:
      targetType: inline
      script: cd one
    ${{ elseif eq(parameters.kind, 'two') }}:
      targetType: inline
      script: cd two
    ${{ else }}:
      targetType: inline
      script: cd three
`,
	})
	cfg := config.Default(dir)
	cfg.Templates.LintReferenced = true
	runner := &recordingRunner{}
	eng, err := lint.New(lint.Options{Registry: rules.Registry(), Config: cfg, RepoRoot: dir, Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	ds, err := eng.Lint(context.Background(), []lint.Input{{Abs: filepath.Join(dir, "azure-pipelines.yml"), Path: "azure-pipelines.yml"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.bash) != 3 || len(ds) != 3 {
		t.Fatalf("expected all three script branches once, got scripts=%d findings=%s", len(runner.bash), summary(ds))
	}
	for i, s := range runner.bash {
		if s.ID != i || s.Raw != []string{"cd one", "cd two", "cd three"}[i] {
			t.Errorf("script %d: %+v", i, s)
		}
	}
}

func TestConditionalMappingSchemaCoverage(t *testing.T) {
	dir := writeFiles(t, map[string]string{"template.yml": `parameters:
- name: flag
  type: boolean
  default: false
steps:
- bash: echo run
  ${{ if parameters.flag }}:
    continueOnError: true
  ${{ else }}:
    continueOnError: maybe
`})
	ds := run(t, dir, nil, nil, "template.yml")
	if len(ds) != 1 || ds[0].RuleID != "ADO006" || ds[0].Line != 10 {
		t.Errorf("invalid alternate mapping value must retain its source location: %+v", ds)
	}
}

func TestKnownConditionalMappingBranches(t *testing.T) {
	for _, flag := range []string{"true", "false"} {
		t.Run(flag, func(t *testing.T) {
			dir := writeFiles(t, map[string]string{
				"main.yml": "trigger: none\nsteps:\n- template: template.yml\n  parameters:\n    flag: " + flag + "\n",
				"template.yml": `parameters:
- name: flag
  type: boolean
steps:
- bash: echo run
  ${{ if parameters.flag }}:
    continueOnError: false
  ${{ else }}:
    continueOnError: maybe
`,
			})
			ds := run(t, dir, nil, nil, "main.yml")
			if flag == "true" {
				if len(ds) != 0 {
					t.Errorf("a known-dead branch must not be linted: %s", summary(ds))
				}
			} else if len(ds) != 1 || ds[0].RuleID != "ADO006" || ds[0].File != "template.yml" || ds[0].Line != 9 {
				t.Errorf("a known-active branch must be linted at its original location: %s", summary(ds))
			}
		})
	}
}

func TestExpansionBudgetIsolation(t *testing.T) {
	step := "- bash: echo skipped\n  ${{ if parameters.flag }}:\n    continueOnError: true\n  ${{ else }}:\n    continueOnError: false\n"
	dir := writeFiles(t, map[string]string{
		"large.yml": "parameters:\n- name: flag\n  type: boolean\n  default: false\nsteps:\n" + strings.Repeat(step, 100),
		"good.yml":  "trigger: none\nsteps:\n- task: MissingVersion\n- bash: cd preserved\n",
	})
	for _, order := range [][]string{{"large.yml", "good.yml"}, {"good.yml", "large.yml"}} {
		t.Run(strings.Join(order, ","), func(t *testing.T) {
			runner := &recordingRunner{}
			eng, err := lint.New(lint.Options{Registry: rules.Registry(), Config: config.Default(dir), RepoRoot: dir, Runner: runner})
			if err != nil {
				t.Fatal(err)
			}
			var inputs []lint.Input
			for _, name := range order {
				inputs = append(inputs, lint.Input{Abs: filepath.Join(dir, name), Path: name})
			}
			var previous string
			for attempt := 0; attempt < 2; attempt++ {
				before := len(runner.bash)
				ds, err := eng.Lint(context.Background(), inputs)
				if err != nil {
					t.Fatal(err)
				}
				counts := map[string]int{}
				for _, d := range ds {
					counts[d.RuleID]++
					if d.RuleID == "ADO208" && (d.File != "large.yml" || !strings.Contains(d.Message, "work limit")) {
						t.Errorf("unexpected expansion failure: %+v", d)
					}
					if d.RuleID != "ADO208" && d.File != "good.yml" {
						t.Errorf("partial expansion leaked a finding: %+v", d)
					}
				}
				if len(ds) != 3 || counts["ADO208"] != 1 || counts["ADO105"] != 1 || counts["ADO701"] != 1 {
					t.Errorf("budget failure hid or duplicated other findings: %s", summary(ds))
				}
				if len(runner.bash)-before != 1 || runner.bash[before].Raw != "cd preserved" {
					t.Fatal("only the healthy entry's script should be linted")
				}
				got := summary(ds)
				if attempt > 0 && got != previous {
					t.Errorf("reusing the engine changed findings:\n%s\n%s", previous, got)
				}
				previous = got
			}
		})
	}
}

func TestRuntimeParameterInsertionContexts(t *testing.T) {
	dir := writeFiles(t, map[string]string{"template.yml": `parameters:
- name: flag
  type: boolean
  default: false
variables:
  arguments: $[ parameters.flag ]
  script: $[ parameters.flag ]
  condition: $[ parameters.flag ]
steps:
- bash: echo hi
  ${{ if true }}:
    condition: eq(parameters.flag, true)
`})
	ds := run(t, dir, nil, nil, "template.yml")
	count := 0
	for _, d := range ds {
		if d.RuleID == "ADO305" {
			count++
		}
		if d.RuleID == "ADO207" || d.RuleID == "ADO301" {
			t.Errorf("runtime usages should be classified, not unused or malformed: %+v", ds)
		}
	}
	if count != 4 {
		t.Errorf("expected 4 runtime parameter warnings, got %d: %+v", count, ds)
	}
}

func TestSingleValueConditionMessage(t *testing.T) {
	dir := writeFiles(t, map[string]string{"template.yml": `parameters:
- name: framework
  type: string
  default: playwright
  values: [playwright]
steps:
- ${{ if eq(parameters.framework, 'playwright') }}:
  - bash: echo run
`})
	ds := run(t, dir, nil, nil, "template.yml")
	if len(ds) != 1 || ds[0].RuleID != "ADO602" || !strings.Contains(ds[0].Message, `parameter "framework" allows only one value`) {
		t.Errorf("single-value parameter diagnostic: %+v", ds)
	}
}
