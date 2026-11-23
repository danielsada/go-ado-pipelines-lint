package schema

import (
	"strings"
	"testing"

	"github.com/danielsada/go-ado-pipelines-lint/internal/yamlx"
)

func validate(t *testing.T, src, def string) []Error {
	t.Helper()
	s, ds := yamlx.Parse("t.yml", "", []byte(src))
	if len(ds) > 0 {
		t.Fatalf("parse: %v", ds)
	}
	v, err := NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	return v.Validate(s.Root, def)
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string // "RULE:substring", empty means valid
	}{
		{"valid steps", "trigger: none\npool:\n  vmImage: ubuntu-latest\nsteps:\n- script: echo hi\n  displayName: Hi\n", nil},
		{"valid stages", `
stages:
- stage: Build
  jobs:
  - job: A
    steps:
    - task: Bash@3
      inputs:
        targetType: inline
        script: echo hi
  - deployment: Deploy
    environment: prod
    strategy:
      runOnce:
        deploy:
          steps:
          - checkout: self
`, nil},
		{"unknown property", "steps:\n- script: echo\n  dispalyName: x\n", []string{`ADO003:did you mean "displayName"`}},
		{"unknown root property", "stages: []\ntriggers: none\n", []string{`ADO003:"triggers"`}},
		{"no root form", "trigger: none\n", []string{"ADO005:one of the keys"}},
		{"invalid enum", "stages:\n- stage: A\n  lockBehavior: random\n  jobs: []\n", []string{"ADO006:sequential"}},
		{"invalid boolean", "steps:\n- script: echo\n  continueOnError: maybe\n", []string{"ADO006:"}},
		{"bad step", "steps:\n- displayName: nothing\n", []string{"ADO005:task, script"}},
		{"unknown task", "steps:\n- task: NotARealTask@1\n", []string{"ADO501:not a built-in task"}},
		{"unknown task version", "steps:\n- task: PowerShell@9\n", []string{"ADO501:known versions: 1, 2"}},
		{"unknown input", "steps:\n- task: Bash@3\n  inputs:\n    targetType: inline\n    scrpt: echo\n", []string{`ADO502:did you mean "script"`}},
		{"missing input", "steps:\n- task: AzureCLI@2\n  inputs:\n    scriptType: bash\n", []string{`ADO503:"azureSubscription"`}},
		{"input alias", "steps:\n- task: AzureCLI@2\n  inputs:\n    connectedServiceNameARM: x\n    scriptType: bash\n    scriptLocation: inlineScript\n    inlineScript: echo\n", nil},
		{"input case", "steps:\n- task: bash@3\n  inputs:\n    TargetType: INLINE\n    Script: echo\n", nil},
		{"deprecated task", "steps:\n- task: AzurePowerShell@2\n  inputs:\n    ScriptPath: a.ps1\n", []string{"ADO007:deprecated"}},
		{"expressions are wildcards", "steps:\n- script: echo\n  continueOnError: ${{ parameters.x }}\n- task: Bash@3\n  inputs:\n    targetType: $(t)\n", nil},
		{"trigger list", "trigger:\n- main\nsteps:\n- bash: echo\n", nil},
		{"variables forms", "variables:\n- group: g\n- name: a\n  value: b\n- template: v.yml\nsteps:\n- pwsh: echo\n", nil},
		{"bad variable", "variables:\n- nam: a\nsteps:\n- pwsh: echo\n", []string{"ADO005:name, group, template"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			errs := validate(t, c.src, "pipeline")
			if len(c.want) == 0 && len(errs) > 0 {
				t.Fatalf("expected no errors, got %v", msgs(errs))
			}
			for _, w := range c.want {
				rule, sub, _ := strings.Cut(w, ":")
				if !hasErr(errs, rule, sub) {
					t.Errorf("missing %s containing %q; got %v", rule, sub, msgs(errs))
				}
			}
		})
	}
}

func TestTemplateDefs(t *testing.T) {
	if errs := validate(t, "parameters:\n- name: a\n  type: string\nsteps:\n- script: echo\n", "stepsTemplate"); len(errs) > 0 {
		t.Fatalf("unexpected: %v", msgs(errs))
	}
	if errs := validate(t, "variables:\n  a: b\n", "variablesTemplate"); len(errs) > 0 {
		t.Fatalf("unexpected: %v", msgs(errs))
	}
}

func TestTasks(t *testing.T) {
	r, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	tasks := r.Tasks()
	if len(tasks) < 100 {
		t.Fatalf("expected many tasks, got %d", len(tasks))
	}
}

func hasErr(errs []Error, rule, sub string) bool {
	for _, e := range errs {
		if e.RuleID() == rule && strings.Contains(e.Message, sub) {
			return true
		}
	}
	return false
}

func msgs(errs []Error) []string {
	var out []string
	for _, e := range errs {
		out = append(out, e.RuleID()+": "+e.Message)
	}
	return out
}
