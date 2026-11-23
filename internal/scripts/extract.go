// Package scripts extracts inline scripts from pipelines and lints them with ShellCheck
// and PSScriptAnalyzer, mapping findings back to positions in the YAML files.
package scripts

import (
	"fmt"
	"regexp"
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
	// segs maps a folded (>) scalar's value back to its source lines.
	segs []segment
	// stepKind is the step keyword or task the script came from.
	stepKind string
}

type segment struct{ start, line, col int }

// Map converts a 1-based position inside the script to a position in the YAML file.
func (s *Script) Map(line, col int) (int, int) {
	if line < 1 {
		line = 1
	}
	if col < 1 {
		col = 1
	}
	switch {
	case s.segs != nil:
		return s.mapFolded(line, col)
	case s.block:
		return s.Line + line - 1, s.indent + col
	case s.singleLine && line == 1:
		return s.Line, s.Column + col - 1
	}
	return s.Line, s.Column
}

func (s *Script) mapFolded(line, col int) (int, int) {
	off := 0
	lines := strings.Split(s.Text, "\n")
	for i := 0; i < line-1 && i < len(lines); i++ {
		off += len(lines[i]) + 1
	}
	off += col - 1
	seg := s.segs[0]
	for _, sg := range s.segs {
		if sg.start <= off {
			seg = sg
		}
	}
	return seg.line, seg.col + off - seg.start
}

// Options controls extraction.
type Options struct {
	// ScriptShell controls plain `script:` steps: "auto" (bash on Linux/macOS pools), "bash" or "skip".
	ScriptShell string
}

// Extractor extracts scripts from pipelines, de-duplicating scripts that appear in several
// expansions of the same template.
type Extractor struct {
	opts     Options
	seen     map[string]*Script
	nonLinux map[string]bool
	scripts  []*Script
}

// NewExtractor creates an extractor.
func NewExtractor(opts Options) *Extractor {
	return &Extractor{opts: opts, seen: map[string]*Script{}, nonLinux: map[string]bool{}}
}

// Scripts returns the extracted scripts, numbered by their position in the result.
func (x *Extractor) Scripts() []*Script {
	var out []*Script
	for _, s := range x.scripts {
		if s.Lang != "" {
			s.ID = len(out)
			out = append(out, s)
		}
	}
	return out
}

// Add extracts the scripts of an expanded pipeline.
func (x *Extractor) Add(p *model.Pipeline) {
	vars := p.DeclaredVariableNames()
	for _, j := range p.AllJobs() {
		linux := isLinuxPool(j, p.PoolUnknown)
		for _, st := range j.Steps {
			if st.Kind == "script" && (x.opts.ScriptShell == "" || x.opts.ScriptShell == "auto") {
				if !linux || windowsOnly(st) {
					x.markNonLinux(p.Doc, st.Value)
					continue
				}
			}
			node, lang, what := x.scriptOf(st, linux)
			if node != nil {
				x.add(p.Doc, node, lang, what, st.Kind, vars)
			}
		}
	}
}

// windowsOnly reports whether a step's condition pins it to Windows agents.
func windowsOnly(st *model.Step) bool {
	return yamlx.IsScalar(st.Condition) && strings.Contains(strings.ToLower(st.Condition.Value), "windows_nt")
}

// markNonLinux records that a script: step also runs on a non-Linux agent, where it is a cmd script,
// and withdraws it if it was already extracted as bash from another expansion of the same template.
func (x *Extractor) markNonLinux(doc *yamlx.Doc, n *yaml.Node) {
	if !yamlx.IsScalar(n) {
		return
	}
	key := originKey(doc, n)
	x.nonLinux[key] = true
	if s, ok := x.seen[key]; ok && s.stepKind == "script" {
		s.Lang = ""
	}
}

func originKey(doc *yamlx.Doc, n *yaml.Node) string {
	o := doc.OriginOf(n)
	return fmt.Sprintf("%s:%d:%d", o.Src.Path, o.Node.Line, o.Node.Column)
}

// isLinuxPool reports whether a job runs on a Linux or macOS agent. With no pool at all, YAML
// pipelines default to ubuntu-latest, unless the pipeline's pool is unknown.
func isLinuxPool(j *model.Job, unknown bool) bool {
	pool := j.EffectivePool()
	if pool == nil {
		return !unknown
	}
	if os := strings.ToLower(pool.OS); os != "" {
		return os == "linux" || os == "macos"
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

func (x *Extractor) add(doc *yamlx.Doc, n *yaml.Node, lang Lang, what, kind string, vars map[string]bool) {
	if !yamlx.IsScalar(n) || yamlx.IsNull(n) {
		return
	}
	o := doc.OriginOf(n)
	orig := o.Node
	if orig.Kind != yaml.ScalarNode || strings.TrimSpace(orig.Value) == "" {
		return
	}
	key := originKey(doc, n)
	if _, ok := x.seen[key]; ok || (kind == "script" && x.nonLinux[key]) {
		return
	}
	s := &Script{Lang: lang, Raw: orig.Value, Source: what, File: o.Src.Path, stepKind: kind,
		Text: Placeholders(orig.Value, vars, lang)}
	locate(s, o.Src, orig)
	x.seen[key] = s
	x.scripts = append(x.scripts, s)
}

func locate(s *Script, src *yamlx.Source, n *yaml.Node) {
	s.Line, s.Column = n.Line, n.Column
	switch n.Style {
	case yaml.LiteralStyle, yaml.FoldedStyle:
		s.Line = n.Line + 1
		s.indent = yamlx.BlockIndent(src.Lines, n.Line)
		s.Column = s.indent + 1
		s.block = n.Style == yaml.LiteralStyle
		if n.Style == yaml.FoldedStyle {
			s.segs = foldedSegments(src.Lines, n, s.indent)
		}
	case yaml.SingleQuotedStyle, yaml.DoubleQuotedStyle:
		s.Column = n.Column + 1
		s.singleLine = !strings.Contains(n.Value, "\n") && (n.Style == yaml.SingleQuotedStyle && !strings.Contains(n.Value, "'") || n.Style == yaml.DoubleQuotedStyle && !strings.ContainsAny(n.Value, `\"`))
	default:
		s.singleLine = !strings.Contains(n.Value, "\n")
	}
}

// foldedSegments locates each source line of a folded block scalar inside its folded value.
func foldedSegments(lines []string, n *yaml.Node, indent int) []segment {
	var segs []segment
	pos := 0
	for i := n.Line; i < len(lines); i++ {
		l := lines[i]
		if strings.TrimSpace(l) == "" {
			continue
		}
		if len(l)-len(strings.TrimLeft(l, " ")) < indent {
			break
		}
		content := strings.TrimRight(l[indent:], " ")
		idx := strings.Index(n.Value[pos:], content)
		if idx < 0 {
			return nil
		}
		segs = append(segs, segment{start: pos + idx, line: i + 1, col: indent + 1})
		pos += idx + len(content)
	}
	return segs
}

// commonCommands are commands often run as $(cmd) command substitutions without arguments.
// A bare $(name) that isn't one of these is treated as an Azure Pipelines macro, because
// variables can also come from variable groups, the pipeline UI or earlier logging commands.
var commonCommands = map[string]bool{
	"pwd": true, "date": true, "whoami": true, "hostname": true, "nproc": true, "uname": true, "id": true,
	"ls": true, "cat": true, "mktemp": true, "arch": true, "tty": true, "true": true, "false": true,
	"umask": true, "groups": true, "jobs": true, "dirs": true, "logname": true, "users": true, "env": true,
	"printenv": true, "getconf": true, "lsb_release": true, "sw_vers": true, "git": true, "ps": true,
}

var macroNameRe = regexp.MustCompile(`^[A-Za-z0-9_.]+$`)

func isMacro(name string, declared map[string]bool) bool {
	if !macroNameRe.MatchString(name) {
		return false
	}
	return strings.Contains(name, ".") || declared[strings.ToLower(name)] || !commonCommands[strings.ToLower(name)]
}

// Placeholders replaces ${{ }} template expressions and $(Name) macros with same-length
// placeholders, so the script parses as the shell sees it after Azure Pipelines substitution
// while keeping every column in place. Placeholders are runs of underscores (a plain word); in
// PowerShell expression positions they start with $ so they parse as a variable. Command substitutions of common commands such as $(pwd) are left alone unless a
// variable with that name is declared.
func Placeholders(s string, declared map[string]bool, lang Lang) string {
	spans, _ := expr.Scan(s)
	b := []byte(s)
	for _, sp := range spans {
		switch {
		case sp.Kind == expr.SpanTemplate:
		case sp.Kind == expr.SpanMacro && isMacro(sp.Expr, declared):
		default:
			continue
		}
		for i := sp.Start; i < sp.End; i++ {
			if b[i] != '\n' {
				b[i] = '_'
			}
		}
		if lang == PowerShell && sp.End-sp.Start > 1 && psExpressionContext(s[:sp.Start]) {
			b[sp.Start] = '$'
		}
	}
	return string(b)
}

var psOperators = map[string]bool{"-eq": true, "-ne": true, "-gt": true, "-ge": true, "-lt": true, "-le": true,
	"-and": true, "-or": true, "-xor": true, "-not": true, "-like": true, "-notlike": true, "-match": true,
	"-notmatch": true, "-contains": true, "-notcontains": true, "-in": true, "-notin": true, "-join": true,
	"-split": true, "-replace": true, "-f": true, "-is": true, "-isnot": true, "-as": true}

// psExpressionContext reports whether text ending right before a placeholder puts PowerShell in
// expression mode (after "(", ",", "=", an operator, ...), where a bare word would be a parse error
// and a $variable placeholder is needed. In command-argument mode (paths such as
// $(Build.SourcesDirectory)/x.ps1) a bare word is right, as a variable would turn "/" into division.
func psExpressionContext(before string) bool {
	t := strings.TrimRight(before, " \t")
	if t == "" || t[len(t)-1] == '$' {
		return false
	}
	if strings.IndexByte("(,=[+*%!<>", t[len(t)-1]) >= 0 {
		return true
	}
	word := t[strings.LastIndexAny(t, " \t\n(")+1:]
	return psOperators[strings.ToLower(word)]
}
