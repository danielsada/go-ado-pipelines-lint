package scripts

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/danielsada/go-ado-pipelines-lint/internal/config"
	"github.com/danielsada/go-ado-pipelines-lint/internal/diag"
)

// Rule IDs emitted by script linting.
const (
	RuleShellCheck       = "ADO701"
	RulePSScriptAnalyzer = "ADO702"
	RuleUnavailable      = "ADO703"
)

// ErrRequiredToolMissing is returned when a linter is missing and --require-script-linters is set.
var ErrRequiredToolMissing = errors.New("required script linter is not installed")

// RecommendedShellCheck is the curated, CI-relevant ShellCheck subset (plus all SC1xxx parse errors).
var RecommendedShellCheck = []string{"SC2086", "SC2046", "SC2164", "SC2155", "SC2181", "SC2034"}

// DefaultExcludedShellCheck are codes that misfire on Azure Pipelines scripts (variables injected
// as environment variables, files sourced from the agent); they are dropped unless explicitly included.
var DefaultExcludedShellCheck = []string{"SC2154", "SC1090", "SC1091"}

// RecommendedPSScriptAnalyzer is the curated PSScriptAnalyzer subset (plus all parse errors).
var RecommendedPSScriptAnalyzer = []string{
	"PSAvoidUsingPlainTextForPassword",
	"PSAvoidUsingConvertToSecureStringWithPlainText",
	"PSAvoidUsingInvokeExpression",
	"PSUseDeclaredVarsMoreThanAssignments",
	"PSAvoidUsingEmptyCatchBlock",
	"PSPossibleIncorrectComparisonWithNull",
}

// LintOptions controls script linting.
type LintOptions struct {
	ShellCheck       config.Tool
	PSScriptAnalyzer config.Tool
	// Require turns a missing tool into an error.
	Require bool
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

func keep(code string, parseError bool, t config.Tool, recommended, excluded []string) bool {
	switch {
	case contains(t.Exclude, code):
		return false
	case contains(t.Include, code):
		return true
	}
	switch t.Profile {
	case "none":
		return false
	case "all":
		return !contains(excluded, code)
	}
	return parseError || contains(recommended, code)
}

func shellcheckSeverity(level string) diag.Severity {
	switch level {
	case "error":
		return diag.Error
	case "warning":
		return diag.Warning
	}
	return diag.Info
}

func psSeverity(level string) diag.Severity {
	switch strings.ToLower(level) {
	case "error", "parseerror":
		return diag.Error
	case "warning":
		return diag.Warning
	}
	return diag.Info
}

// Lint runs the linters over the scripts and converts findings to diagnostics.
func Lint(ctx context.Context, runner Runner, scripts []*Script, opts LintOptions) ([]diag.Diagnostic, error) {
	var bash, ps []*Script
	for _, s := range scripts {
		switch s.Lang {
		case Bash:
			bash = append(bash, s)
		case PowerShell:
			ps = append(ps, s)
		}
	}
	var out []diag.Diagnostic
	var errs []error
	if len(bash) > 0 && (opts.ShellCheck.Profile != "none" || len(opts.ShellCheck.Include) > 0) {
		fs, err := runner.ShellCheck(ctx, bash)
		out = append(out, convert(fs, scripts, RuleShellCheck, func(f Finding) (bool, diag.Severity) {
			parse := strings.HasPrefix(f.Code, "SC1") && f.Level == "error"
			return keep(f.Code, parse, opts.ShellCheck, RecommendedShellCheck, DefaultExcludedShellCheck), shellcheckSeverity(f.Level)
		})...)
		d, e := toolProblem(err, "ShellCheck (shellcheck)", "https://www.shellcheck.net", bash, opts.Require)
		out, errs = append(out, d...), append(errs, e)
	}
	if len(ps) > 0 && (opts.PSScriptAnalyzer.Profile != "none" || len(opts.PSScriptAnalyzer.Include) > 0) {
		fs, err := runner.PSScriptAnalyzer(ctx, ps)
		out = append(out, convert(fs, scripts, RulePSScriptAnalyzer, func(f Finding) (bool, diag.Severity) {
			parse := strings.EqualFold(f.Level, "ParseError")
			return keep(f.Code, parse, opts.PSScriptAnalyzer, RecommendedPSScriptAnalyzer, nil), psSeverity(f.Level)
		})...)
		d, e := toolProblem(err, "PSScriptAnalyzer (pwsh with the PSScriptAnalyzer module)", "https://learn.microsoft.com/powershell/utility-modules/psscriptanalyzer/overview", ps, opts.Require)
		out, errs = append(out, d...), append(errs, e)
	}
	return out, errors.Join(errs...)
}

func convert(fs []Finding, scripts []*Script, rule string, filter func(Finding) (bool, diag.Severity)) []diag.Diagnostic {
	var out []diag.Diagnostic
	for _, f := range fs {
		if f.ScriptID < 0 || f.ScriptID >= len(scripts) {
			continue
		}
		ok, sev := filter(f)
		if !ok {
			continue
		}
		s := scripts[f.ScriptID]
		line, col := s.Map(f.Line, f.Column)
		d := diag.Diagnostic{RuleID: rule, SubRule: f.Code, Severity: sev, Message: f.Message,
			Location: diag.Location{File: s.File, Line: line, Column: col}}
		if f.EndLine == f.Line && f.EndColumn > f.Column {
			d.EndLine, d.EndColumn = s.Map(f.EndLine, f.EndColumn)
		}
		out = append(out, d)
	}
	return out
}

func toolProblem(err error, tool, url string, scripts []*Script, require bool) ([]diag.Diagnostic, error) {
	if err == nil {
		return nil, nil
	}
	first := scripts[0]
	loc := diag.Location{File: first.File, Line: first.Line, Column: first.Column}
	if !errors.Is(err, ErrToolMissing) {
		return []diag.Diagnostic{{RuleID: RuleUnavailable, Severity: diag.Warning, Location: loc,
			Message: fmt.Sprintf("%s failed; %d script(s) were not linted: %v", tool, len(scripts), err)}}, nil
	}
	sev := diag.Info
	var retErr error
	if require {
		sev = diag.Error
		retErr = fmt.Errorf("%w: %s", ErrRequiredToolMissing, tool)
	}
	return []diag.Diagnostic{{RuleID: RuleUnavailable, Severity: sev, Location: loc,
		Message: fmt.Sprintf("%s is not installed; %d inline script(s) were not linted. Install it from %s, or use --no-scripts", tool, len(scripts), url)}}, retErr
}
