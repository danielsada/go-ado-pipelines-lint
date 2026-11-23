package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielsada/go-ado-pipelines-lint/internal/scripts"
)

type noTools struct{}

func (noTools) ShellCheck(context.Context, []*scripts.Script) ([]scripts.Finding, error) {
	return nil, scripts.ErrToolMissing
}

func (noTools) PSScriptAnalyzer(context.Context, []*scripts.Script) ([]scripts.Finding, error) {
	return nil, scripts.ErrToolMissing
}

func setup(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
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

func runCLI(t *testing.T, dir string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	app := &App{Version: "1.0.0", Stdout: &out, Stderr: &errb, Dir: dir, Runner: noTools{}}
	code := app.Run(context.Background(), args)
	return code, out.String(), errb.String()
}

const pipeline = `trigger: none
pool:
  vmImage: ubuntu-latest
steps:
- template: templates/build.yml
  parameters:
    config: Release
- script: echo done
`

const tpl = `parameters:
- name: config
  type: string
  default: Debug
steps:
- script: make CONFIG=${{ parameters.config }}
`

func TestLintExitCodes(t *testing.T) {
	dir := setup(t, map[string]string{
		"azure-pipelines.yml":    pipeline,
		"templates/build.yml":    tpl,
		".azure-pipelines/b.yml": "steps:\n- script: echo\n  continueOnError: true\n",
	})
	code, out, _ := runCLI(t, dir, "lint")
	if code != ExitFindings || !strings.Contains(out, ".azure-pipelines/b.yml:1:1: warning [ADO401]") {
		t.Errorf("lint: code %d\n%s", code, out)
	}
	if strings.Contains(out, "templates/build.yml") && !strings.Contains(out, "ADO703") {
		t.Errorf("template should not be linted standalone:\n%s", out)
	}
	if code, out, _ = runCLI(t, dir, "lint", "--fail-on", "error"); code != ExitOK {
		t.Errorf("--fail-on error: code %d\n%s", code, out)
	}
	if code, _, _ = runCLI(t, dir, "lint", "azure-pipelines.yml"); code != ExitOK {
		t.Errorf("clean file: code %d", code)
	}
	if c, _, errOut := runCLI(t, dir, "lint", "--require-script-linters", "azure-pipelines.yml"); c != ExitError || !strings.Contains(errOut, "required script linter") {
		t.Errorf("--require-script-linters: code %d %s", c, errOut)
	}
	if code, _, _ = runCLI(t, dir, "lint", "--no-scripts", "--require-script-linters", "azure-pipelines.yml"); code != ExitOK {
		t.Errorf("--no-scripts: code %d", code)
	}
	if c, _, errOut := runCLI(t, dir, "lint", "--disable", "NOPE"); c != ExitError || !strings.Contains(errOut, "unknown rule") {
		t.Errorf("bad rule: code %d %s", c, errOut)
	}
	if code, _, _ = runCLI(t, dir, "lint", "--format", "xml"); code != ExitError {
		t.Errorf("bad format: code %d", code)
	}
	if code, _, _ = runCLI(t, dir, "lint", "missing.yml"); code != ExitError {
		t.Errorf("missing file: code %d", code)
	}
}

func TestLintConfigAndFormats(t *testing.T) {
	dir := setup(t, map[string]string{
		"azure-pipelines.yml": "steps:\n- script: echo\n",
		".adolint.yaml":       "rules:\n  ADO401: off\n",
	})
	if code, out, _ := runCLI(t, dir, "lint", "--no-scripts"); code != ExitOK || !strings.Contains(out, "No problems found") {
		t.Errorf("config disable: code %d %s", code, out)
	}
	dir = setup(t, map[string]string{"azure-pipelines.yml": "steps:\n- task: Foo\n"})
	code, out, _ := runCLI(t, dir, "lint", "-f", "sarif", "--no-scripts")
	var log struct {
		Version string `json:"version"`
		Runs    []struct {
			Results []struct {
				RuleID string `json:"ruleId"`
			} `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal([]byte(out), &log); err != nil || code != ExitFindings || log.Version != "2.1.0" || len(log.Runs[0].Results) != 2 {
		t.Errorf("sarif: code %d err %v\n%s", code, err, out)
	}
	if code, out, _ = runCLI(t, dir, "lint", "-f", "json", "-o", "report.json", "--no-scripts"); code != ExitFindings || out != "" {
		t.Errorf("-o: code %d out %q", code, out)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "report.json")); err != nil || !bytes.Contains(b, []byte(`"ruleId": "ADO105"`)) {
		t.Errorf("report.json: %v %s", err, b)
	}
}

func TestParseCommand(t *testing.T) {
	dir := setup(t, map[string]string{"azure-pipelines.yml": pipeline, "templates/build.yml": tpl})
	code, out, _ := runCLI(t, dir, "parse", "--expand", "azure-pipelines.yml")
	if code != ExitOK || !strings.Contains(out, "make CONFIG=Release") || strings.Contains(out, "template:") {
		t.Errorf("parse --expand: code %d\n%s", code, out)
	}
	code, out, _ = runCLI(t, dir, "parse", "-f", "json", "azure-pipelines.yml")
	if code != ExitOK || !strings.Contains(out, `"template": "templates/build.yml"`) {
		t.Errorf("parse json: code %d\n%s", code, out)
	}
	dir = setup(t, map[string]string{"bad.yml": "a: b: c\n"})
	if code, _, errOut := runCLI(t, dir, "parse", "bad.yml"); code != ExitFindings || !strings.Contains(errOut, "ADO001") {
		t.Errorf("parse invalid: code %d %s", code, errOut)
	}
}

func TestExtractCommand(t *testing.T) {
	dir := setup(t, map[string]string{"azure-pipelines.yml": pipeline, "templates/build.yml": tpl})
	code, out, _ := runCLI(t, dir, "extract", "azure-pipelines.yml")
	if code != ExitOK || !strings.Contains(out, "templates/build.yml:6:11 (script step, bash)") || !strings.Contains(out, "make CONFIG=________________________") {
		t.Errorf("extract: code %d\n%s", code, out)
	}
	code, _, _ = runCLI(t, dir, "extract", "--out", "scripts", "azure-pipelines.yml")
	b, err := os.ReadFile(filepath.Join(dir, "scripts", "manifest.json"))
	if code != ExitOK || err != nil || !bytes.Contains(b, []byte(`"file": "templates/build.yml"`)) {
		t.Errorf("extract --out: code %d err %v\n%s", code, err, b)
	}
}

func TestRulesExplainVersion(t *testing.T) {
	code, out, _ := runCLI(t, t.TempDir(), "rules")
	if code != ExitOK || !strings.Contains(out, "ADO402  info (off)") || !strings.Contains(out, "ADO701") {
		t.Errorf("rules: code %d\n%s", code, out)
	}
	code, out, _ = runCLI(t, t.TempDir(), "rules", "-f", "json")
	var rules []map[string]any
	if err := json.Unmarshal([]byte(out), &rules); err != nil || code != ExitOK || len(rules) < 30 {
		t.Errorf("rules json: %v %d", err, len(rules))
	}
	code, out, _ = runCLI(t, t.TempDir(), "explain", "ado701/SC2086")
	if code != ExitOK || !strings.Contains(out, "ADO701 shellcheck") || !strings.Contains(out, "Docs:") {
		t.Errorf("explain: code %d\n%s", code, out)
	}
	if code, _, _ = runCLI(t, t.TempDir(), "explain", "ADO999"); code != ExitError {
		t.Errorf("explain unknown: code %d", code)
	}
	for _, args := range [][]string{{"version"}, {"--version"}} {
		if code, out, _ = runCLI(t, t.TempDir(), args...); code != ExitOK || out != "1.0.0\n" {
			t.Errorf("%v: code %d out %q", args, code, out)
		}
	}
}
