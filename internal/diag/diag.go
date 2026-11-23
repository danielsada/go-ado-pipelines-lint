// Package diag defines the diagnostic types shared by the parser, expander and linters.
package diag

import (
	"fmt"
	"sort"
	"strings"
)

// Severity is the importance of a diagnostic.
type Severity int

// Severity levels, ordered from least to most severe.
const (
	Off Severity = iota
	Info
	Warning
	Error
)

// String returns the lowercase name of the severity.
func (s Severity) String() string {
	switch s {
	case Info:
		return "info"
	case Warning:
		return "warning"
	case Error:
		return "error"
	default:
		return "off"
	}
}

// ParseSeverity parses "off", "info", "warning" or "error" (case-insensitive).
func ParseSeverity(s string) (Severity, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off", "none", "disable", "disabled":
		return Off, nil
	case "info", "note":
		return Info, nil
	case "warning", "warn":
		return Warning, nil
	case "error", "err":
		return Error, nil
	}
	return Off, fmt.Errorf("invalid severity %q (want off, info, warning or error)", s)
}

// Location is a 1-based position range inside a file.
type Location struct {
	File      string `json:"file"`
	Line      int    `json:"line"`
	Column    int    `json:"column"`
	EndLine   int    `json:"endLine,omitempty"`
	EndColumn int    `json:"endColumn,omitempty"`
}

// Diagnostic is a single finding.
type Diagnostic struct {
	RuleID   string   `json:"ruleId"`
	SubRule  string   `json:"subRule,omitempty"`
	Severity Severity `json:"-"`
	Message  string   `json:"message"`
	Location
}

// FullID returns the rule ID including the sub-rule, e.g. "ADO701/SC2086".
func (d Diagnostic) FullID() string {
	if d.SubRule == "" {
		return d.RuleID
	}
	return d.RuleID + "/" + d.SubRule
}

// Sort orders diagnostics by file, line, column and rule ID.
func Sort(ds []Diagnostic) {
	sort.SliceStable(ds, func(i, j int) bool {
		a, b := ds[i], ds[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Column != b.Column {
			return a.Column < b.Column
		}
		if a.FullID() != b.FullID() {
			return a.FullID() < b.FullID()
		}
		return a.Message < b.Message
	})
}

// Dedupe removes exact duplicates (same rule, location and message), keeping order.
func Dedupe(ds []Diagnostic) []Diagnostic {
	type key struct {
		id, msg, file string
		line, col     int
	}
	seen := make(map[key]bool, len(ds))
	out := ds[:0]
	for _, d := range ds {
		k := key{d.FullID(), d.Message, d.File, d.Line, d.Column}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, d)
	}
	return out
}

// Count returns the number of diagnostics at each severity.
func Count(ds []Diagnostic) map[Severity]int {
	m := map[Severity]int{}
	for _, d := range ds {
		m[d.Severity]++
	}
	return m
}

// Max returns the highest severity among the diagnostics, or Off if empty.
func Max(ds []Diagnostic) Severity {
	m := Off
	for _, d := range ds {
		if d.Severity > m {
			m = d.Severity
		}
	}
	return m
}
