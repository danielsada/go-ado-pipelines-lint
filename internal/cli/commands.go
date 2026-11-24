package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"

	"github.com/danielsada/go-ado-pipelines-lint/internal/diag"
	"github.com/danielsada/go-ado-pipelines-lint/internal/discover"
	"github.com/danielsada/go-ado-pipelines-lint/internal/lint"
	"github.com/danielsada/go-ado-pipelines-lint/internal/report"
	"github.com/danielsada/go-ado-pipelines-lint/internal/scripts"
)

type lintFlags struct {
	common
	format        string
	output        string
	enable        []string
	disable       []string
	failOn        string
	noScripts     bool
	requireLinter bool
}

func (a *App) lintCmd() *cobra.Command {
	f := &lintFlags{}
	cmd := &cobra.Command{
		Use:   "lint [paths...]",
		Short: "Lint pipeline files",
		Long: `Lint Azure Pipelines YAML files. Without arguments, the working directory is searched
for azure-pipelines*.yml files and YAML files under .azure-pipelines/, .azuredevops/,
.pipelines/ and azure-pipelines/. Files referenced as templates are linted as part of the
pipelines that include them.
Set templates.lintReferenced: true in .adolint.yaml to also check their uncalled branches.

Exit codes: 0 no findings at or above --fail-on, 1 findings, 2 usage, I/O or configuration error.`,
		RunE: func(cmd *cobra.Command, args []string) error { return a.runLint(cmd, f, args) },
	}
	f.register(cmd)
	fl := cmd.Flags()
	fl.StringVarP(&f.format, "format", "f", "text", "output format: text, json or sarif")
	fl.StringVarP(&f.output, "output", "o", "", "write the report to a file instead of stdout")
	fl.StringSliceVar(&f.enable, "enable", nil, "enable rules (comma-separated IDs) at their default severity")
	fl.StringSliceVar(&f.disable, "disable", nil, "disable rules (comma-separated IDs)")
	fl.StringVar(&f.failOn, "fail-on", "warning", "lowest severity that causes exit code 1: error, warning, info or none")
	fl.BoolVar(&f.noScripts, "no-scripts", false, "do not lint inline scripts with ShellCheck/PSScriptAnalyzer")
	fl.BoolVar(&f.requireLinter, "require-script-linters", false, "fail (exit 2) if ShellCheck or PSScriptAnalyzer is needed but not installed")
	return cmd
}

func overrides(reg *lint.Registry, enable, disable []string) (map[string]diag.Severity, error) {
	out := map[string]diag.Severity{}
	for _, id := range enable {
		id = strings.ToUpper(strings.TrimSpace(id))
		r, ok := reg.Get(id)
		if !ok {
			return nil, fmt.Errorf("unknown rule %q in --enable", id)
		}
		out[id] = r.Meta().Severity
	}
	for _, id := range disable {
		id = strings.ToUpper(strings.TrimSpace(id))
		if !reg.Has(id) {
			return nil, fmt.Errorf("unknown rule %q in --disable", id)
		}
		out[id] = diag.Off
	}
	return out, nil
}

func parseFailOn(s string) (diag.Severity, error) {
	if strings.EqualFold(s, "none") {
		return diag.Off, nil
	}
	sev, err := diag.ParseSeverity(s)
	if err != nil || sev == diag.Off {
		return diag.Off, fmt.Errorf("invalid --fail-on %q (want error, warning, info or none)", s)
	}
	return sev, nil
}

func (a *App) inputs(s *session, args []string) ([]lint.Input, error) {
	files, err := discover.Find(s.dir, args)
	if err != nil {
		return nil, err
	}
	var inputs []lint.Input
	for _, f := range files {
		if rel, err := filepath.Rel(s.cfg.Dir, f); err == nil && !strings.HasPrefix(rel, "..") && s.cfg.Ignored(rel) {
			continue
		}
		inputs = append(inputs, lint.Input{Abs: f, Path: display(s.dir, f)})
	}
	return inputs, nil
}

func (a *App) runLint(cmd *cobra.Command, f *lintFlags, args []string) error {
	failOn, err := parseFailOn(f.failOn)
	if err != nil {
		return usageErr("%v", err)
	}
	if !contains(report.Formats, f.format) {
		return usageErr("unknown --format %q (want %s)", f.format, strings.Join(report.Formats, ", "))
	}
	s, err := a.session(&f.common)
	if err != nil {
		return usageErr("%v", err)
	}
	ov, err := overrides(s.registry, f.enable, f.disable)
	if err != nil {
		return usageErr("%v", err)
	}
	eng, err := a.engine(s, &f.common, func(o *lint.Options) {
		o.Overrides, o.NoScripts, o.RequireScriptLinters = ov, f.noScripts, f.requireLinter
	})
	if err != nil {
		return usageErr("%v", err)
	}
	inputs, err := a.inputs(s, args)
	if err != nil {
		return usageErr("%v", err)
	}
	if len(inputs) == 0 {
		_, _ = fmt.Fprintln(a.Stderr, "No Azure Pipelines YAML files found.")
		return nil
	}
	ds, toolErr := eng.Lint(cmd.Context(), inputs)
	if err := a.writeReport(f, s, ds, len(inputs)); err != nil {
		return usageErr("%v", err)
	}
	if toolErr != nil {
		return &exitError{code: ExitError, err: toolErr}
	}
	if failOn != diag.Off && diag.Max(ds) >= failOn {
		return &exitError{code: ExitFindings}
	}
	return nil
}

func (a *App) writeReport(f *lintFlags, s *session, ds []diag.Diagnostic, files int) error {
	w := a.Stdout
	if f.output != "" {
		path := f.output
		if !filepath.IsAbs(path) {
			path = filepath.Join(s.dir, path)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		fh, err := a.Create(path)
		if err != nil {
			return err
		}
		defer func() { _ = fh.Close() }()
		w = fh
	}
	uri := func(file string) string {
		abs := file
		if !filepath.IsAbs(filepath.FromSlash(file)) {
			abs = filepath.Join(s.dir, filepath.FromSlash(file))
		}
		return display(s.root, abs)
	}
	return report.Write(w, f.format, ds, s.registry, report.Options{Version: a.Version, URI: uri, Files: files})
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

type parseFlags struct {
	common
	expand bool
	format string
}

func (a *App) parseCmd() *cobra.Command {
	f := &parseFlags{}
	cmd := &cobra.Command{
		Use:   "parse <file>",
		Short: "Print the parsed (and optionally template-expanded) pipeline",
		Args:  cobra.ExactArgs(1),
		RunE:  func(_ *cobra.Command, args []string) error { return a.runParse(f, args[0]) },
	}
	f.register(cmd)
	cmd.Flags().BoolVar(&f.expand, "expand", false, "expand templates, parameters and ${{ }} expressions")
	cmd.Flags().StringVarP(&f.format, "format", "f", "yaml", "output format: yaml or json")
	return cmd
}

func (a *App) loadOne(c *common, file string) (*session, *lint.Engine, *lint.Entry, error) {
	s, err := a.session(c)
	if err != nil {
		return nil, nil, nil, usageErr("%v", err)
	}
	eng, err := a.engine(s, c, nil)
	if err != nil {
		return nil, nil, nil, usageErr("%v", err)
	}
	abs := file
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(s.dir, file)
	}
	entries, srcs, err := eng.Load([]lint.Input{{Abs: abs, Path: display(s.dir, abs)}})
	if err != nil {
		return nil, nil, nil, usageErr("%v", err)
	}
	if len(srcs[0].ParseDiags) > 0 && srcs[0].Invalid {
		_ = report.Text(a.Stderr, srcs[0].ParseDiags, report.Options{})
		return nil, nil, nil, &exitError{code: ExitFindings}
	}
	return s, eng, entries[0], nil
}

func (a *App) runParse(f *parseFlags, file string) error {
	if f.format != "yaml" && f.format != "json" {
		return usageErr("unknown --format %q (want yaml or json)", f.format)
	}
	_, eng, en, err := a.loadOne(&f.common, file)
	if err != nil {
		return err
	}
	root := en.Src.Root
	if f.expand {
		eng.Expand(en)
		root = en.Result.Doc.Root
		if len(en.Result.Diags) > 0 {
			_ = report.Text(a.Stderr, en.Result.Diags, report.Options{})
		}
	}
	if root == nil {
		return nil
	}
	return writeNode(a.Stdout, root, f.format)
}

func writeNode(w io.Writer, root *yaml.Node, format string) error {
	if format == "json" {
		var v any
		if err := root.Decode(&v); err != nil {
			return err
		}
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, string(b))
		return err
	}
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	if err := enc.Encode(stripComments(root)); err != nil {
		return err
	}
	return enc.Close()
}

func stripComments(n *yaml.Node) *yaml.Node {
	c := *n
	c.HeadComment, c.LineComment, c.FootComment = "", "", ""
	c.Content = make([]*yaml.Node, len(n.Content))
	for i, ch := range n.Content {
		c.Content[i] = stripComments(ch)
	}
	return &c
}

type extractFlags struct {
	common
	out string
}

func (a *App) extractCmd() *cobra.Command {
	f := &extractFlags{}
	cmd := &cobra.Command{
		Use:   "extract <file>",
		Short: "Extract inline scripts (after template expansion) for inspection or other tools",
		Long: `Extract inline bash and PowerShell scripts from a pipeline after template expansion.
Azure Pipelines syntax is replaced by same-length placeholders, exactly as adolint lints them.
With --out, each script is written to a file and manifest.json maps them back to YAML positions.`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error { return a.runExtract(f, args[0]) },
	}
	f.register(cmd)
	cmd.Flags().StringVar(&f.out, "out", "", "directory to write scripts and manifest.json to")
	return cmd
}

type manifestEntry struct {
	Path   string `json:"path"`
	Lang   string `json:"lang"`
	Source string `json:"source"`
	File   string `json:"file"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
}

func (a *App) runExtract(f *extractFlags, file string) error {
	s, eng, en, err := a.loadOne(&f.common, file)
	if err != nil {
		return err
	}
	eng.Expand(en)
	x := scripts.NewExtractor(scripts.Options{ScriptShell: s.cfg.Scripts.ScriptShell})
	for _, m := range en.Pipelines() {
		x.Add(m)
	}
	found := x.Scripts()
	if f.out == "" {
		for _, sc := range found {
			if _, err = fmt.Fprintf(a.Stdout, "# --- %s:%d:%d (%s, %s)\n%s\n", sc.File, sc.Line, sc.Column, sc.Source, sc.Lang, strings.TrimRight(sc.Text, "\n")); err != nil {
				return err
			}
		}
		return nil
	}
	out := f.out
	if !filepath.IsAbs(out) {
		out = filepath.Join(s.dir, out)
	}
	if err = os.MkdirAll(out, 0o755); err != nil {
		return usageErr("%v", err)
	}
	manifest := []manifestEntry{}
	for _, sc := range found {
		base := strings.TrimSuffix(filepath.Base(sc.File), filepath.Ext(sc.File))
		name := fmt.Sprintf("%03d-%s-L%d%s", sc.ID+1, base, sc.Line, sc.Lang.Ext())
		if err = os.WriteFile(filepath.Join(out, name), []byte(sc.Text), 0o644); err != nil {
			return usageErr("%v", err)
		}
		manifest = append(manifest, manifestEntry{Path: name, Lang: string(sc.Lang), Source: sc.Source, File: sc.File, Line: sc.Line, Column: sc.Column})
	}
	b, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(out, "manifest.json"), append(b, '\n'), 0o644); err != nil {
		return usageErr("%v", err)
	}
	_, err = fmt.Fprintf(a.Stdout, "Wrote %d script(s) to %s\n", len(found), out)
	return err
}

func (a *App) rulesCmd() *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "rules",
		Short: "List all rules with their default severity",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			reg := a.registryFor()
			switch format {
			case "json":
				return writeRulesJSON(a.Stdout, reg)
			case "text":
				return writeRulesText(a.Stdout, reg)
			}
			return usageErr("unknown --format %q (want text or json)", format)
		},
	}
	cmd.Flags().StringVarP(&format, "format", "f", "text", "output format: text or json")
	return cmd
}

func writeRulesText(w io.Writer, reg *lint.Registry) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "ID\tSEVERITY\tNAME\tSUMMARY\tDOCS"); err != nil {
		return err
	}
	for _, r := range reg.All() {
		m := r.Meta()
		sev := m.Severity.String()
		if m.DisabledByDefault {
			sev += " (off)"
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", m.ID, sev, m.Name, m.Summary, m.DocsURL()); err != nil {
			return err
		}
	}
	return tw.Flush()
}

type ruleJSON struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Severity         string `json:"severity"`
	EnabledByDefault bool   `json:"enabledByDefault"`
	Summary          string `json:"summary"`
	Description      string `json:"description"`
	HelpURI          string `json:"helpUri,omitempty"`
	DocsURL          string `json:"docsUrl"`
}

func writeRulesJSON(w io.Writer, reg *lint.Registry) error {
	var out []ruleJSON
	for _, r := range reg.All() {
		m := r.Meta()
		out = append(out, ruleJSON{ID: m.ID, Name: m.Name, Severity: m.Severity.String(), EnabledByDefault: !m.DisabledByDefault,
			Summary: m.Summary, Description: m.Description, HelpURI: m.HelpURI, DocsURL: m.DocsURL()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func (a *App) explainCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "explain <rule-id>",
		Short: "Explain a rule, with examples",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			reg := a.registryFor()
			id := strings.ToUpper(strings.TrimSpace(args[0]))
			if before, _, ok := strings.Cut(id, "/"); ok {
				id = before
			}
			r, ok := reg.Get(id)
			if !ok {
				return usageErr("unknown rule %q; run 'adolint rules' to list rules", args[0])
			}
			return explain(a.Stdout, r.Meta())
		},
	}
}

func explain(w io.Writer, m *lint.Meta) error {
	def := m.Severity.String()
	if m.DisabledByDefault {
		def += ", disabled by default"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s (%s)\n\n%s\n\n%s\n", m.ID, m.Name, def, m.Summary, m.Description)
	if m.Bad != "" {
		fmt.Fprintf(&b, "\nProblem:\n\n%s\n", indent(m.Bad))
	}
	if m.Good != "" {
		fmt.Fprintf(&b, "\nInstead:\n\n%s\n", indent(m.Good))
	}
	if m.HelpURI != "" {
		fmt.Fprintf(&b, "\nReference: %s\n", m.HelpURI)
	}
	fmt.Fprintf(&b, "Docs: %s\nQuestions or false positives: %s\n", m.DocsURL(), lint.IssuesURL)
	fmt.Fprintf(&b, "\nDisable with `# adolint:disable-next-line %s` or `rules: {%s: off}` in .adolint.yaml.\n", m.ID, m.ID)
	_, err := io.WriteString(w, b.String())
	return err
}

func indent(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = "    " + l
	}
	return strings.Join(lines, "\n")
}
