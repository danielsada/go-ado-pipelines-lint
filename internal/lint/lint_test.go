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
	dir := writeFiles(t, map[string]string{"p.yml": "steps:\n- script: echo\n", "v.yml": "variables:\n  a: b\n"})
	if got := summary(run(t, dir, nil, nil, "p.yml", "v.yml")); got != "p.yml:1:ADO401:warning" {
		t.Errorf("got %s", got)
	}
}
