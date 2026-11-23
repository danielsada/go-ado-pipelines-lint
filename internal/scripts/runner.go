package scripts

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"unicode/utf16"
)

// ErrToolMissing is returned (wrapped) when an external linter is not installed.
var ErrToolMissing = errors.New("tool not installed")

// Finding is a raw finding from an external linter, positioned within a script.
type Finding struct {
	ScriptID int
	// Code is the tool's rule code, e.g. "SC2086" or "PSAvoidUsingInvokeExpression".
	Code string
	// Level is the tool's severity name.
	Level     string
	Line      int
	Column    int
	EndLine   int
	EndColumn int
	Message   string
}

// Runner runs external script linters.
type Runner interface {
	ShellCheck(ctx context.Context, scripts []*Script) ([]Finding, error)
	PSScriptAnalyzer(ctx context.Context, scripts []*Script) ([]Finding, error)
}

// ExecRunner runs the real tools found on PATH.
type ExecRunner struct {
	// Dir is the working directory for the tools (so .shellcheckrc and PSScriptAnalyzer settings apply).
	Dir string
}

type shellcheckOutput struct {
	Comments []struct {
		Line      int    `json:"line"`
		EndLine   int    `json:"endLine"`
		Column    int    `json:"column"`
		EndColumn int    `json:"endColumn"`
		Level     string `json:"level"`
		Code      int    `json:"code"`
		Message   string `json:"message"`
	} `json:"comments"`
}

// ShellCheck runs `shellcheck -f json1 -s bash -` on each script, in parallel.
func (r ExecRunner) ShellCheck(ctx context.Context, scripts []*Script) ([]Finding, error) {
	path, err := exec.LookPath("shellcheck")
	if err != nil {
		return nil, fmt.Errorf("shellcheck: %w", ErrToolMissing)
	}
	var (
		mu       sync.Mutex
		findings []Finding
		firstErr error
		wg       sync.WaitGroup
	)
	sem := make(chan struct{}, runtime.NumCPU())
	for _, s := range scripts {
		wg.Add(1)
		sem <- struct{}{}
		go func(s *Script) {
			defer wg.Done()
			defer func() { <-sem }()
			fs, err := r.shellcheckOne(ctx, path, s)
			mu.Lock()
			defer mu.Unlock()
			if err != nil && firstErr == nil {
				firstErr = err
			}
			findings = append(findings, fs...)
		}(s)
	}
	wg.Wait()
	return findings, firstErr
}

func (r ExecRunner) shellcheckOne(ctx context.Context, path string, s *Script) ([]Finding, error) {
	cmd := exec.CommandContext(ctx, path, "--format=json1", "--shell=bash", "--severity=style", "-")
	cmd.Dir = r.Dir
	cmd.Stdin = strings.NewReader(s.Text)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if err != nil && (!errors.As(err, &exitErr) || exitErr.ExitCode() != 1) {
		return nil, fmt.Errorf("shellcheck: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	var out shellcheckOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return nil, fmt.Errorf("shellcheck: parsing output: %w", err)
	}
	fs := make([]Finding, 0, len(out.Comments))
	for _, c := range out.Comments {
		fs = append(fs, Finding{ScriptID: s.ID, Code: "SC" + strconv.Itoa(c.Code), Level: c.Level,
			Line: c.Line, Column: c.Column, EndLine: c.EndLine, EndColumn: c.EndColumn, Message: c.Message})
	}
	return fs, nil
}

// psProgram runs PSScriptAnalyzer on a JSON array of {id, script} read from stdin.
const psProgram = `
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Console]::InputEncoding = [Text.Encoding]::UTF8
[Console]::OutputEncoding = [Text.Encoding]::UTF8
if (-not (Get-Module -ListAvailable -Name PSScriptAnalyzer)) { '{"missing":true,"results":[]}'; exit 0 }
Import-Module PSScriptAnalyzer
# Windows PowerShell 5.1 emits a JSON array as a single object; enumerate it explicitly.
$items = @(([Console]::In.ReadToEnd() | ConvertFrom-Json) | ForEach-Object { $_ })
$out = [System.Collections.Generic.List[object]]::new()
foreach ($i in $items) {
  try { $res = @(Invoke-ScriptAnalyzer -ScriptDefinition $i.script) }
  catch {
    $out.Add([pscustomobject]@{ id = $i.id; rule = 'PSScriptAnalyzerFailure'; severity = 'Error'; line = 1; column = 1; endLine = 1; endColumn = 1; message = $_.Exception.Message })
    continue
  }
  foreach ($d in $res) {
    $e = $d.Extent
    $out.Add([pscustomobject]@{
      id = $i.id; rule = [string]$d.RuleName; severity = [string]$d.Severity
      line = [int]$d.Line; column = [int]$d.Column
      endLine = $(if ($e) { [int]$e.EndLineNumber } else { 0 })
      endColumn = $(if ($e) { [int]$e.EndColumnNumber } else { 0 })
      message = [string]$d.Message })
  }
}
ConvertTo-Json -InputObject @{ missing = $false; results = @($out) } -Depth 4 -Compress
`

type psOutput struct {
	Missing bool `json:"missing"`
	Results []struct {
		ID        int    `json:"id"`
		Rule      string `json:"rule"`
		Severity  string `json:"severity"`
		Line      int    `json:"line"`
		Column    int    `json:"column"`
		EndLine   int    `json:"endLine"`
		EndColumn int    `json:"endColumn"`
		Message   string `json:"message"`
	} `json:"results"`
}

func findPowerShell() (string, error) {
	if p, err := exec.LookPath("pwsh"); err == nil {
		return p, nil
	}
	if runtime.GOOS == "windows" {
		if p, err := exec.LookPath("powershell"); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("pwsh: %w", ErrToolMissing)
}

func encodeCommand(s string) string {
	u := utf16.Encode([]rune(s))
	b := make([]byte, len(u)*2)
	for i, c := range u {
		b[2*i], b[2*i+1] = byte(c), byte(c>>8)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// PSScriptAnalyzer runs Invoke-ScriptAnalyzer on all scripts in a single PowerShell process.
func (r ExecRunner) PSScriptAnalyzer(ctx context.Context, scripts []*Script) ([]Finding, error) {
	ps, err := findPowerShell()
	if err != nil {
		return nil, err
	}
	type item struct {
		ID     int    `json:"id"`
		Script string `json:"script"`
	}
	items := make([]item, 0, len(scripts))
	for _, s := range scripts {
		items = append(items, item{s.ID, s.Text})
	}
	input, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, ps, "-NoProfile", "-NonInteractive", "-EncodedCommand", encodeCommand(psProgram))
	cmd.Dir = r.Dir
	cmd.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("PSScriptAnalyzer: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	var out psOutput
	if err := json.Unmarshal(bytes.TrimPrefix(bytes.TrimSpace(stdout.Bytes()), []byte("\xef\xbb\xbf")), &out); err != nil {
		return nil, fmt.Errorf("PSScriptAnalyzer: parsing output: %w", err)
	}
	if out.Missing {
		return nil, fmt.Errorf("PSScriptAnalyzer module: %w", ErrToolMissing)
	}
	fs := make([]Finding, 0, len(out.Results))
	for _, res := range out.Results {
		fs = append(fs, Finding{ScriptID: res.ID, Code: res.Rule, Level: res.Severity, Line: res.Line,
			Column: res.Column, EndLine: res.EndLine, EndColumn: res.EndColumn, Message: res.Message})
	}
	return fs, nil
}
