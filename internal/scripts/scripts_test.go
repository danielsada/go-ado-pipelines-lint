package scripts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/danielsada/go-ado-pipelines-lint/internal/config"
	"github.com/danielsada/go-ado-pipelines-lint/internal/diag"
	"github.com/danielsada/go-ado-pipelines-lint/internal/expr"
	"github.com/danielsada/go-ado-pipelines-lint/internal/model"
	"github.com/danielsada/go-ado-pipelines-lint/internal/template"
	"github.com/danielsada/go-ado-pipelines-lint/internal/yamlx"
)

func TestPlaceholders(t *testing.T) {
	in := "cd $(Build.SourcesDirectory) && echo $(pwd) ${{ parameters.x }} $(myVar) $(other) $(date) $(two words)"
	got := Placeholders(in, map[string]bool{"myvar": true}, Bash)
	if len(got) != len(in) {
		t.Fatalf("length changed: %d -> %d", len(in), len(got))
	}
	u := func(s string) string { return strings.Repeat("_", len(s)) }
	want := "cd " + u("$(Build.SourcesDirectory)") + " && echo $(pwd) " + u("${{ parameters.x }}") + " " + u("$(myVar)") + " " + u("$(other)") + " $(date) $(two words)"
	if got != want {
		t.Errorf("Placeholders:\n got %q\nwant %q", got, want)
	}
	ps := Placeholders("(Get-Date).AddDays(${{ parameters.days }}) $(Get-Date) & $(Build.SourcesDirectory)/x.ps1 $${{ p.x }} -eq ${{ p.y }}", nil, PowerShell)
	if want := "(Get-Date).AddDays($" + u("{{ parameters.days }}") + ") $(Get-Date) & " + u("$(Build.SourcesDirectory)") + "/x.ps1 $" + u("${{ p.x }}") + " -eq $" + u("{{ p.y }}"); ps != want {
		t.Errorf("PowerShell placeholders:\n got %q\nwant %q", ps, want)
	}
}

func TestPowerShellCastPlaceholders(t *testing.T) {
	for _, before := range []string{"[int]", "[System.Int32] ", "[Nullable[int]]", "$x = [int]", "($x -eq [int]"} {
		in := before + "${{ parameters.x }}"
		got := Placeholders(in, nil, PowerShell)
		want := before + "$" + strings.Repeat("_", len("${{ parameters.x }}")-1)
		if got != want || len(got) != len(in) {
			t.Errorf("%q => %q, want %q", in, got, want)
		}
	}
}

func TestPowerShellQuotedPlaceholders(t *testing.T) {
	expression := "$(Build.DefinitionName)"
	for _, prefix := range []string{
		`Write-Host "##[error]`,
		`Write-Host "##[warning]`,
		`Write-Host "##vso[task.logissue type=error]`,
		`Write-Host '##[section]`,
		"Write-Host @\"\n\"quoted\" ##[error]",
		"Write-Host \"escaped `\" ##[error]",
	} {
		in := prefix + expression + ": failed"
		want := prefix + strings.Repeat("_", len(expression)) + ": failed"
		if got := Placeholders(in, nil, PowerShell); got != want {
			t.Errorf("%q => %q, want %q", in, got, want)
		}
	}
	in := `Write-Host "$([int]${{ parameters.x }})"`
	want := `Write-Host "$([int]$` + strings.Repeat("_", len("${{ parameters.x }}")-1) + `)"`
	if got := Placeholders(in, nil, PowerShell); got != want {
		t.Errorf("cast in string subexpression: got %q, want %q", got, want)
	}
	in = "Write-Host @\"\n$([int]${{ parameters.x }}) ##[error]$(Build.DefinitionName)\n\"@"
	want = "Write-Host @\"\n$([int]$" + strings.Repeat("_", len("${{ parameters.x }}")-1) + ") ##[error]" + strings.Repeat("_", len(expression)) + "\n\"@"
	if got := Placeholders(in, nil, PowerShell); got != want {
		t.Errorf("cast in here-string subexpression: got %q, want %q", got, want)
	}
}

func FuzzPlaceholders(f *testing.F) {
	for _, seed := range []string{
		"echo $(Build.SourcesDirectory) ${{ parameters.name }} $(pwd)",
		"Write-Host \"##[error]$(Build.DefinitionName): failed\"",
		"Write-Host \"$([int]${{ parameters.number }})\"",
		"$[ format('{0}', '${{ parameters.name }}') ]",
		"echo ${{\r\n parameters.name\r\n}}",
		"echo caf\u00e9 ${{ parameters.name }}\n",
		"${{ unterminated $(Build.Name)\n",
		"Write-Host @\"\n$([int]${{ parameters.number }})\n\"@",
		"$($(0))",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > 4096 {
			t.Skip()
		}
		mutable := make([]bool, len(input))
		spans, _ := expr.Scan(input)
		for _, span := range spans {
			replace := span.Kind == expr.SpanTemplate || span.Kind == expr.SpanMacro && isMacro(span.Expr, nil)
			if !replace {
				continue
			}
			for i := span.Start; i < span.End; i++ {
				mutable[i] = true
			}
		}
		for _, lang := range []Lang{Bash, PowerShell} {
			got := Placeholders(input, nil, lang)
			if len(got) != len(input) {
				t.Fatalf("%s changed byte offsets: %q => %q", lang, input, got)
			}
			for i := range len(input) {
				if (input[i] == '\n' || input[i] == '\r') && got[i] != input[i] {
					t.Fatalf("%s changed a line ending at byte %d: %q => %q", lang, i, input, got)
				}
				if got[i] != input[i] && got[i] != '_' && got[i] != '$' {
					t.Fatalf("%s corrupted a non-placeholder byte at %d: %q => %q", lang, i, input, got)
				}
				if got[i] != input[i] && !mutable[i] {
					t.Fatalf("%s changed a byte outside an Azure expression at %d: %q => %q", lang, i, input, got)
				}
			}
		}
	})
}

func TestPowerShellPlaceholderSyntax(t *testing.T) {
	if !scriptToolAvailable(t, "pwsh") {
		t.Skip("pwsh is not installed")
	}
	cases := []struct{ Name, Text string }{
		{"logging", `Write-Host "##[error]$(Build.DefinitionName): failed"`},
		{"single quotes", `Write-Host 'can''t ##[section]$(Build.DefinitionName): failed'`},
		{"escaped quotes", "Write-Host \"escaped `\" ##[warning]$(Build.DefinitionName): failed\""},
		{"casts", "$number = [int]${{ parameters.number }}\n[int]${{ parameters.number }}"},
		{"nested subexpressions", `Write-Host "$([int]${{ parameters.number }}): $([Math]::Abs([int]${{ parameters.number }}))"`},
		{"here string", "Write-Host @\"\n\"quoted\" ##[error]$(Build.DefinitionName)\n$([int]${{ parameters.number }}) ##[error]$(Build.DefinitionName)\n\"@"},
		{"line comment", "# unmatched \" and [\n[int]${{ parameters.number }}"},
		{"block comment", "<#\nunmatched \" and [\n#>\n[int]${{ parameters.number }}"},
		{"comment opener inside comment", "<#\n<# another opener\n#>\n[int]${{ parameters.number }}"},
	}
	for i := range cases {
		cases[i].Text = Placeholders(cases[i].Text, nil, PowerShell)
	}
	input, err := json.Marshal(cases)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "pwsh", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", `
$failed = $false
foreach ($sample in ([Console]::In.ReadToEnd() | ConvertFrom-Json)) {
    $tokens = $null
    $parseErrors = $null
    $null = [System.Management.Automation.Language.Parser]::ParseInput($sample.Text, [ref]$tokens, [ref]$parseErrors)
    if ($parseErrors.Count -gt 0) {
        [Console]::Error.WriteLine($sample.Name + ': ' + ($parseErrors.Message -join '; '))
        $failed = $true
    }
}
if ($failed) { exit 1 }
`)
	cmd.Stdin = strings.NewReader(string(input))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("masked scripts must parse without execution: %v\n%s", err, output)
	}
}

func TestPinnedTaskScripts(t *testing.T) {
	scripts := extract(t, "steps:\n- task: Bash@3.250.1\n  inputs:\n    targetType: inline\n    script: echo bash\n- task: PowerShell@2.250.1\n  inputs:\n    targetType: inline\n    script: Write-Host test\n", Options{})
	if len(scripts) != 2 || scripts[0].Lang != Bash || scripts[1].Lang != PowerShell {
		t.Fatalf("full pins should retain script extraction: %+v", scripts)
	}
}

const pipelineYAML = `trigger: none
variables:
  configuration: Release
jobs:
- job: Linux
  pool:
    vmImage: ubuntu-latest
  steps:
  - script: |
      echo $(configuration)
      cd $DIR
  - bash: echo "${{ parameters.missing }}" $X
  - task: Bash@3
    inputs:
      targetType: inline
      script: ls $Y
  - task: Bash@3
    inputs:
      targetType: filePath
      filePath: run.sh
- job: Windows
  pool:
    vmImage: windows-latest
  steps:
  - script: echo %PATH%
  - pwsh: Write-Output 'hi'
  - task: AzureCLI@2
    inputs:
      azureSubscription: x
      scriptType: pscore
      scriptLocation: inlineScript
      inlineScript: |
        az account show
`

func extract(t *testing.T, src string, opts Options) []*Script {
	t.Helper()
	s, ds := yamlx.Parse("azure-pipelines.yml", "", []byte(src))
	if len(ds) > 0 {
		t.Fatal(ds)
	}
	res := template.ExpandPipeline(s, template.Options{NoTemplates: true})
	x := NewExtractor(opts)
	x.Add(model.Build(res.Doc, false))
	return x.Scripts()
}

func TestMixedPoolScriptIsSkipped(t *testing.T) {
	scripts := extract(t, `trigger: none
jobs:
- ${{ each img in split('ubuntu-latest,windows-latest', ',') }}:
  - job: J_${{ replace(img, '-', '_') }}
    pool:
      vmImage: ${{ img }}
    steps:
    - script: cd x64
    - bash: cd y
`, Options{})
	if len(scripts) != 1 || scripts[0].Source != "bash step" || scripts[0].ID != 0 {
		t.Fatalf("got %+v, want only the bash step", scripts)
	}
}

func TestFoldedAndWindowsOnly(t *testing.T) {
	scripts := extract(t, `trigger: none
steps:
- script: >
    docker run --rm
    --volume $AAA:/a
    $CCC sleep 1
- script: cd x64
  condition: eq(variables['Agent.OS'], 'Windows_NT')
`, Options{})
	if len(scripts) != 1 {
		t.Fatalf("got %d scripts, want only the folded one", len(scripts))
	}
	s := scripts[0]
	off := strings.Index(s.Text, "$AAA")
	if l, c := s.Map(1, off+1); l != 5 || c != 14 {
		t.Errorf("folded Map = %d:%d, want 5:14", l, c)
	}
	off = strings.Index(s.Text, "$CCC")
	if l, c := s.Map(1, off+1); l != 6 || c != 5 {
		t.Errorf("folded Map = %d:%d, want 6:5", l, c)
	}
}

func TestExtract(t *testing.T) {
	scripts := extract(t, pipelineYAML, Options{})
	type want struct {
		lang         Lang
		line, column int
		text         string
	}
	wants := []want{
		{Bash, 10, 7, "echo " + strings.Repeat("_", len("$(configuration)")) + "\ncd $DIR\n"},
		{Bash, 12, 11, `echo "` + strings.Repeat("_", len("${{ parameters.missing }}")) + `" $X`},
		{Bash, 16, 15, "ls $Y"},
		{PowerShell, 26, 11, "Write-Output 'hi'"},
		{PowerShell, 33, 9, "az account show\n"},
	}
	if len(scripts) != len(wants) {
		for _, s := range scripts {
			t.Logf("%s %s:%d:%d %q", s.Lang, s.File, s.Line, s.Column, s.Text)
		}
		t.Fatalf("got %d scripts, want %d", len(scripts), len(wants))
	}
	for i, w := range wants {
		s := scripts[i]
		if s.Lang != w.lang || s.Line != w.line || s.Column != w.column || s.Text != w.text {
			t.Errorf("script %d = %s %d:%d %q, want %s %d:%d %q", i, s.Lang, s.Line, s.Column, s.Text, w.lang, w.line, w.column, w.text)
		}
	}
	if l, c := scripts[0].Map(2, 4); l != 11 || c != 10 {
		t.Errorf("block Map(2,4) = %d:%d, want 11:10", l, c)
	}
	if l, c := scripts[2].Map(1, 4); l != 16 || c != 18 {
		t.Errorf("plain Map(1,4) = %d:%d, want 16:18", l, c)
	}
	if got := extract(t, pipelineYAML, Options{ScriptShell: "skip"}); len(got) != 4 {
		t.Errorf("scriptShell=skip: got %d scripts, want 4", len(got))
	}
	if got := extract(t, pipelineYAML, Options{ScriptShell: "bash"}); len(got) != 6 {
		t.Errorf("scriptShell=bash: got %d scripts, want 6", len(got))
	}
}

type fakeRunner struct {
	sc, ps       []Finding
	scErr, psErr error
}

func (f fakeRunner) ShellCheck(context.Context, []*Script) ([]Finding, error) { return f.sc, f.scErr }

func (f fakeRunner) PSScriptAnalyzer(context.Context, []*Script) ([]Finding, error) {
	return f.ps, f.psErr
}

func ids(ds []diag.Diagnostic) []string {
	var out []string
	for _, d := range ds {
		out = append(out, fmt.Sprintf("%s@%d:%d:%s", d.FullID(), d.Line, d.Column, d.Severity))
	}
	return out
}

func TestLintProfiles(t *testing.T) {
	scripts := []*Script{
		{ID: 0, Lang: Bash, File: "a.yml", Line: 10, Column: 7, block: true, indent: 6},
		{ID: 1, Lang: PowerShell, File: "a.yml", Line: 20, Column: 9, singleLine: true},
	}
	runner := fakeRunner{
		sc: []Finding{
			{ScriptID: 0, Code: "SC2086", Level: "info", Line: 2, Column: 4},
			{ScriptID: 0, Code: "SC2154", Level: "warning", Line: 1, Column: 1},
			{ScriptID: 0, Code: "SC1009", Level: "error", Line: 3, Column: 1},
			{ScriptID: 0, Code: "SC2250", Level: "style", Line: 1, Column: 2},
		},
		ps: []Finding{
			{ScriptID: 1, Code: "PSAvoidUsingWriteHost", Level: "Warning", Line: 1, Column: 1},
			{ScriptID: 1, Code: "MissingEndCurlyBrace", Level: "ParseError", Line: 1, Column: 5},
			{ScriptID: 1, Code: "PSAvoidUsingInvokeExpression", Level: "Warning", Line: 1, Column: 3},
		},
	}
	cases := []struct {
		name string
		opts LintOptions
		want string
	}{
		{"recommended", LintOptions{},
			"ADO701/SC1009@12:7:error ADO702/MissingEndCurlyBrace@20:13:error ADO702/PSAvoidUsingInvokeExpression@20:11:warning"},
		{"all", LintOptions{ShellCheck: config.Tool{Profile: "all"}, PSScriptAnalyzer: config.Tool{Profile: "all"}},
			"ADO701/SC2086@11:10:info ADO701/SC1009@12:7:error ADO701/SC2250@10:8:info ADO702/PSAvoidUsingWriteHost@20:9:warning ADO702/MissingEndCurlyBrace@20:13:error ADO702/PSAvoidUsingInvokeExpression@20:11:warning"},
		{"include and exclude", LintOptions{ShellCheck: config.Tool{Include: []string{"SC2154"}, Exclude: []string{"SC2086"}}, PSScriptAnalyzer: config.Tool{Profile: "none"}},
			"ADO701/SC2154@10:7:warning ADO701/SC1009@12:7:error"},
	}
	for _, c := range cases {
		ds, err := Lint(context.Background(), runner, scripts, c.opts)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(ids(ds), " "); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.want)
		}
	}
}

func TestToolMissing(t *testing.T) {
	scripts := []*Script{{ID: 0, Lang: Bash, File: "a.yml", Line: 3, Column: 5}}
	runner := fakeRunner{scErr: fmt.Errorf("shellcheck: %w", ErrToolMissing)}
	ds, err := Lint(context.Background(), runner, scripts, LintOptions{})
	if err != nil || len(ds) != 1 || ds[0].RuleID != RuleUnavailable || ds[0].Severity != diag.Info {
		t.Fatalf("got %v, %v", ids(ds), err)
	}
	ds, err = Lint(context.Background(), runner, scripts, LintOptions{Require: true})
	if !errors.Is(err, ErrRequiredToolMissing) || ds[0].Severity != diag.Error {
		t.Fatalf("require: got %v, %v", ids(ds), err)
	}
}

func scriptToolAvailable(t *testing.T, tool string) bool {
	t.Helper()
	if _, err := exec.LookPath(tool); err == nil {
		return true
	}
	if os.Getenv("ADOLINT_INTEGRATION") != "" {
		t.Errorf("%s is required when ADOLINT_INTEGRATION is set", tool)
	}
	return false
}

// TestRealTools runs the installed linters. It is skipped when they are missing unless
// ADOLINT_INTEGRATION is set (as in CI), in which case missing tools fail the test.
func TestRealTools(t *testing.T) {
	scripts := extract(t, pipelineYAML, Options{})
	opts := LintOptions{Require: os.Getenv("ADOLINT_INTEGRATION") != "", ShellCheck: config.Tool{Profile: "all"}}
	if scriptToolAvailable(t, "shellcheck") {
		ds, err := Lint(context.Background(), ExecRunner{}, scripts[:3], opts)
		if err != nil {
			t.Fatal(err)
		}
		got := strings.Join(ids(ds), " ")
		for _, w := range []string{"ADO701/SC2164@11:7", "ADO701/SC2086@11:10", "ADO701/SC2086@12:44", "ADO701/SC2086@16:18"} {
			if !strings.Contains(got, w) {
				t.Errorf("shellcheck: missing %s in %s", w, got)
			}
		}
	}
	if scriptToolAvailable(t, "pwsh") {
		ps := []*Script{{ID: 0, Lang: PowerShell, File: "a.yml", Line: 5, Column: 3, singleLine: true, Text: `Invoke-Expression "x"; $unused = 1`}}
		ds, err := Lint(context.Background(), ExecRunner{}, ps, opts)
		if err != nil {
			t.Fatal(err)
		}
		got := strings.Join(ids(ds), " ")
		if len(ds) == 1 && ds[0].RuleID == RuleUnavailable {
			t.Skip("PSScriptAnalyzer module is not installed")
		}
		for _, w := range []string{"ADO702/PSAvoidUsingInvokeExpression@5:3", "ADO702/PSUseDeclaredVarsMoreThanAssignments@5:26"} {
			if !strings.Contains(got, w) {
				t.Errorf("PSScriptAnalyzer: missing %s in %s", w, got)
			}
		}
	}
}
