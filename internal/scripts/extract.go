// Package scripts extracts inline scripts from pipelines and lints them with ShellCheck
// and PSScriptAnalyzer, mapping findings back to positions in the YAML files.
package scripts

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/danielsada/go-ado-pipelines-lint/internal/expr"
	"github.com/danielsada/go-ado-pipelines-lint/internal/model"
	"github.com/danielsada/go-ado-pipelines-lint/internal/yamlx"
)

// Lang is a script language.
type Lang string

// Script languages.
const (
	Bash       Lang = "bash"
	PowerShell Lang = "powershell"
)

// Ext returns the file extension for the language.
func (l Lang) Ext() string {
	if l == PowerShell {
		return ".ps1"
	}
	return ".sh"
}

// Script is an extracted inline script.
type Script struct {
	ID   int
	Lang Lang
	// Text is the script with Azure Pipelines syntax replaced by same-length placeholders.
	Text string
	// Raw is the script text as written in the YAML file.
	Raw string
	// Source describes where the script came from, e.g. "bash step" or "Bash@3 task".
	Source string
	File   string
	// Line and Column locate the first character of the script in the file.
	Line, Column int
	block        bool
	indent       int
	singleLine   bool
}

// Map converts a 1-based position inside the script to a position in the YAML file.
func (s *Script) Map(line, col int) (int, int) {
	if line < 1 {
		line = 1
	}
	if col < 1 {
		col = 1
	}
	switch {
	case s.block:
		return s.Line + line - 1, s.indent + col
	case s.singleLine && line == 1:
		return s.Line, s.Column + col - 1
	}
	return s.Line, s.Column
}

// Options controls extraction.
type Options struct {
	// ScriptShell controls plain `script:` steps: "auto" (bash on Linux/macOS pools), "bash" or "skip".
	ScriptShell string
}

// Extractor extracts scripts from pipelines, de-duplicating scripts that appear in several
// expansions of the same template.
type Extractor struct {
	opts    Options
	seen    map[string]*Script
	Scripts []*Script
}

// NewExtractor creates an extractor.
func NewExtractor(opts Options) *Extractor {
	return &Extractor{opts: opts, seen: map[string]*Script{}}
}

// Add extracts the scripts of an expanded pipeline.
func (x *Extractor) Add(p *model.Pipeline) {
	vars := p.DeclaredVariableNames()
	for _, j := range p.AllJobs() {
		linux := isLinuxPool(j)
		for _, st := range j.Steps {
			node, lang, what := x.scriptOf(st, linux)
			if node != nil {
				x.add(p.Doc, node, lang, what, vars)
			}
		}
	}
}

// isLinuxPool reports whether a job runs on a Microsoft-hosted Linux or macOS image.
// With no pool at all, YAML pipelines default to ubuntu-latest.
func isLinuxPool(j *model.Job) bool {
	pool := j.EffectivePool()
	if pool == nil {
		return true
	}
	img := strings.ToLower(pool.VMImage)
	return strings.HasPrefix(img, "ubuntu") || strings.HasPrefix(img, "macos")
}

func (x *Extractor) scriptOf(st *model.Step, linux bool) (*yaml.Node, Lang, string) {
	switch st.Kind {
	case "bash":
		return st.Value, Bash, "bash step"
	case "pwsh", "powershell":
		return st.Value, PowerShell, st.Kind + " step"
	case "script":
		if x.opts.ScriptShell == "bash" || (x.opts.ScriptShell != "skip" && linux) {
			return st.Value, Bash, "script step"
		}
	case "task":
		return taskScript(st)
	}
	return nil, "", ""
}

func inline(st *model.Step, typeInput string) bool {
	t := strings.ToLower(st.InputStr(typeInput))
	return t == "" || t == "inline"
}

func taskScript(st *model.Step) (*yaml.Node, Lang, string) {
	name, version := st.TaskName()
	what := st.TaskRef() + " task"
	switch {
	case name == "bash" && version == "3" && inline(st, "targetType"):
		return st.Input("script"), Bash, what
	case name == "powershell" && version == "2" && inline(st, "targetType"):
		return st.Input("script"), PowerShell, what
	case name == "azurecli" && version == "2":
		loc := strings.ToLower(st.InputStr("scriptLocation"))
		if loc != "" && loc != "inlinescript" {
			return nil, "", ""
		}
		switch strings.ToLower(st.InputStr("scriptType")) {
		case "bash":
			return st.Input("inlineScript"), Bash, what
		case "ps", "pscore":
			return st.Input("inlineScript"), PowerShell, what
		}
	case name == "azurepowershell" && (version == "4" || version == "5"):
		if strings.EqualFold(st.InputStr("ScriptType"), "InlineScript") {
			return st.Input("Inline"), PowerShell, what
		}
	}
	return nil, "", ""
}

func (x *Extractor) add(doc *yamlx.Doc, n *yaml.Node, lang Lang, what string, vars map[string]bool) {
	if !yamlx.IsScalar(n) || yamlx.IsNull(n) {
		return
	}
	o := doc.OriginOf(n)
	orig := o.Node
	if orig.Kind != yaml.ScalarNode || strings.TrimSpace(orig.Value) == "" {
		return
	}
	key := fmt.Sprintf("%s:%d:%d", o.Src.Path, orig.Line, orig.Column)
	if prev, ok := x.seen[key]; ok {
		if prev.Lang != lang && lang == Bash {
			prev.Lang = Bash
		}
		return
	}
	s := &Script{ID: len(x.Scripts), Lang: lang, Raw: orig.Value, Source: what, File: o.Src.Path,
		Text: Placeholders(orig.Value, vars)}
	locate(s, o.Src, orig)
	x.seen[key] = s
	x.Scripts = append(x.Scripts, s)
}

func locate(s *Script, src *yamlx.Source, n *yaml.Node) {
	s.Line, s.Column = n.Line, n.Column
	switch n.Style {
	case yaml.LiteralStyle, yaml.FoldedStyle:
		s.Line = n.Line + 1
		s.indent = yamlx.BlockIndent(src.Lines, n.Line)
		s.Column = s.indent + 1
		s.block = n.Style == yaml.LiteralStyle
	case yaml.SingleQuotedStyle, yaml.DoubleQuotedStyle:
		s.Column = n.Column + 1
		s.singleLine = !strings.Contains(n.Value, "\n") && (n.Style == yaml.SingleQuotedStyle && !strings.Contains(n.Value, "'") || n.Style == yaml.DoubleQuotedStyle && !strings.ContainsAny(n.Value, `\"`))
	default:
		s.singleLine = !strings.Contains(n.Value, "\n")
	}
}

// Placeholders replaces ${{ }} template expressions and $(Name.With.Dots) / declared-variable macros
// with same-length underscore runs, so the script parses as the shell sees it after
// Azure Pipelines substitution while keeping every column in place. Command substitutions
// like $(pwd) are left alone unless a variable with that name is declared.
func Placeholders(s string, declared map[string]bool) string {
	spans, _ := expr.Scan(s)
	b := []byte(s)
	for _, sp := range spans {
		switch sp.Kind {
		case expr.SpanTemplate:
		case expr.SpanMacro:
			if !strings.Contains(sp.Expr, ".") && !declared[strings.ToLower(sp.Expr)] {
				continue
			}
		default:
			continue
		}
		for i := sp.Start; i < sp.End; i++ {
			if b[i] != '\n' {
				b[i] = '_'
			}
		}
	}
	return string(b)
}
