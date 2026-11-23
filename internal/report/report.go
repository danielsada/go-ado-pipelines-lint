// Package report writes diagnostics as text, JSON or SARIF.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/danielsada/go-ado-pipelines-lint/internal/diag"
	"github.com/danielsada/go-ado-pipelines-lint/internal/lint"
)

// Formats lists the supported output formats.
var Formats = []string{"text", "json", "sarif"}

// Options configures report writing.
type Options struct {
	Version string
	// URI converts a diagnostic file path to a repository-relative, slash-separated URI (SARIF).
	URI func(string) string
	// Files is the number of files linted (for the text summary).
	Files int
}

// Write writes diagnostics in the given format.
func Write(w io.Writer, format string, ds []diag.Diagnostic, reg *lint.Registry, opts Options) error {
	switch format {
	case "text", "":
		return Text(w, ds, opts)
	case "json":
		return JSON(w, ds, reg, opts)
	case "sarif":
		return SARIF(w, ds, reg, opts)
	}
	return fmt.Errorf("unknown format %q (want %s)", format, strings.Join(Formats, ", "))
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// Text writes "file:line:col: severity [ID] message" lines and a summary.
func Text(w io.Writer, ds []diag.Diagnostic, opts Options) error {
	for _, d := range ds {
		if _, err := fmt.Fprintf(w, "%s:%d:%d: %s [%s] %s\n", d.File, d.Line, d.Column, d.Severity, d.FullID(), d.Message); err != nil {
			return err
		}
	}
	c := diag.Count(ds)
	files := ""
	if opts.Files > 0 {
		files = " in " + plural(opts.Files, "file")
	}
	if len(ds) == 0 {
		_, err := fmt.Fprintf(w, "No problems found%s.\n", files)
		return err
	}
	_, err := fmt.Fprintf(w, "\n%s, %s, %s%s.\n", plural(c[diag.Error], "error"), plural(c[diag.Warning], "warning"), plural(c[diag.Info], "info message"), files)
	if err != nil {
		return err
	}
	return ruleLinks(w, ds)
}

// ruleLinks lists the documentation page of every rule that was reported.
func ruleLinks(w io.Writer, ds []diag.Diagnostic) error {
	seen := map[string]bool{}
	var ids []string
	for _, d := range ds {
		if !seen[d.RuleID] {
			seen[d.RuleID] = true
			ids = append(ids, d.RuleID)
		}
	}
	sort.Strings(ids)
	if _, err := fmt.Fprintln(w, "\nRule docs (questions or false positives: "+lint.IssuesURL+"):"); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := fmt.Fprintf(w, "  %s  %s\n", id, lint.DocsURL(id)); err != nil {
			return err
		}
	}
	return nil
}

type jsonDiag struct {
	RuleID    string `json:"ruleId"`
	SubRule   string `json:"subRule,omitempty"`
	Name      string `json:"name,omitempty"`
	Severity  string `json:"severity"`
	Message   string `json:"message"`
	File      string `json:"file"`
	Line      int    `json:"line"`
	Column    int    `json:"column"`
	EndLine   int    `json:"endLine,omitempty"`
	EndColumn int    `json:"endColumn,omitempty"`
	DocsURL   string `json:"docsUrl"`
}

type jsonReport struct {
	Version     string         `json:"version"`
	Summary     map[string]int `json:"summary"`
	Diagnostics []jsonDiag     `json:"diagnostics"`
}

// JSON writes a JSON report.
func JSON(w io.Writer, ds []diag.Diagnostic, reg *lint.Registry, opts Options) error {
	c := diag.Count(ds)
	r := jsonReport{Version: opts.Version, Diagnostics: []jsonDiag{},
		Summary: map[string]int{"error": c[diag.Error], "warning": c[diag.Warning], "info": c[diag.Info]}}
	for _, d := range ds {
		jd := jsonDiag{RuleID: d.RuleID, SubRule: d.SubRule, Severity: d.Severity.String(), Message: d.Message,
			File: d.File, Line: d.Line, Column: d.Column, EndLine: d.EndLine, EndColumn: d.EndColumn, DocsURL: lint.DocsURL(d.RuleID)}
		if rule, ok := reg.Get(d.RuleID); ok {
			jd.Name = rule.Meta().Name
		}
		r.Diagnostics = append(r.Diagnostics, jd)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}
