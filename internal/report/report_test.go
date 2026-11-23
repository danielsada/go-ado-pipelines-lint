package report

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/danielsada/go-ado-pipelines-lint/internal/diag"
	"github.com/danielsada/go-ado-pipelines-lint/internal/lint"
)

var update = flag.Bool("update", false, "update golden files")

func sample() ([]diag.Diagnostic, *lint.Registry) {
	reg := lint.NewRegistry(
		metaRule{lint.Meta{ID: "ADO103", Name: "unknown-dependency", Severity: diag.Error, Summary: "Unknown dependency.", HelpURI: "https://example.com/ado103"}},
		metaRule{lint.Meta{ID: "ADO701", Name: "shellcheck", Severity: diag.Warning, Summary: "ShellCheck finding."}},
	)
	ds := []diag.Diagnostic{
		{RuleID: "ADO103", Severity: diag.Error, Message: `stage "Test" depends on unknown stage "Biuld"`,
			Location: diag.Location{File: "azure-pipelines.yml", Line: 12, Column: 14, EndLine: 12, EndColumn: 19}},
		{RuleID: "ADO701", SubRule: "SC2086", Severity: diag.Info, Message: "Double quote to prevent globbing and word splitting.",
			Location: diag.Location{File: "templates/build.yml", Line: 7, Column: 9}},
	}
	return ds, reg
}

type metaRule struct{ m lint.Meta }

func (r metaRule) Meta() *lint.Meta { return &r.m }

func TestGolden(t *testing.T) {
	ds, reg := sample()
	for _, format := range Formats {
		t.Run(format, func(t *testing.T) {
			var buf bytes.Buffer
			if err := Write(&buf, format, ds, reg, Options{Version: "1.2.3", Files: 2}); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join("testdata", "report."+format+".golden")
			if *update {
				if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run go test ./internal/report -update)", err)
			}
			if !bytes.Equal(bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n")), buf.Bytes()) {
				t.Errorf("output differs from %s:\n%s", path, buf.String())
			}
		})
	}
}

func TestEmptyText(t *testing.T) {
	var buf bytes.Buffer
	if err := Text(&buf, nil, Options{Files: 1}); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "No problems found in 1 file.\n" {
		t.Errorf("got %q", buf.String())
	}
}
