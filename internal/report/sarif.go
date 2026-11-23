package report

import (
	"encoding/json"
	"io"
	"path/filepath"
	"strings"

	"github.com/danielsada/go-ado-pipelines-lint/internal/diag"
	"github.com/danielsada/go-ado-pipelines-lint/internal/lint"
)

const (
	sarifSchema = "https://json.schemastore.org/sarif-2.1.0.json"
	toolURI     = "https://github.com/danielsada/go-ado-pipelines-lint"
)

type sarifLog struct {
	Schema  string     `json:"$schema"`
	Version string     `json:"version"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool    sarifTool     `json:"tool"`
	Results []sarifResult `json:"results"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Name           string      `json:"name"`
	Version        string      `json:"version,omitempty"`
	InformationURI string      `json:"informationUri"`
	Rules          []sarifRule `json:"rules"`
}

type sarifText struct {
	Text string `json:"text"`
}

type sarifRule struct {
	ID                   string        `json:"id"`
	Name                 string        `json:"name,omitempty"`
	ShortDescription     sarifText     `json:"shortDescription"`
	FullDescription      *sarifText    `json:"fullDescription,omitempty"`
	HelpURI              string        `json:"helpUri,omitempty"`
	DefaultConfiguration sarifRuleConf `json:"defaultConfiguration"`
}

type sarifRuleConf struct {
	Level string `json:"level"`
}

type sarifResult struct {
	RuleID    string          `json:"ruleId"`
	RuleIndex int             `json:"ruleIndex"`
	Level     string          `json:"level"`
	Message   sarifText       `json:"message"`
	Locations []sarifLocation `json:"locations"`
}

type sarifLocation struct {
	PhysicalLocation sarifPhysical `json:"physicalLocation"`
}

type sarifPhysical struct {
	ArtifactLocation sarifArtifact `json:"artifactLocation"`
	Region           sarifRegion   `json:"region"`
}

type sarifArtifact struct {
	URI       string `json:"uri"`
	URIBaseID string `json:"uriBaseId,omitempty"`
}

type sarifRegion struct {
	StartLine   int `json:"startLine"`
	StartColumn int `json:"startColumn,omitempty"`
	EndLine     int `json:"endLine,omitempty"`
	EndColumn   int `json:"endColumn,omitempty"`
}

func sarifLevel(s diag.Severity) string {
	switch s {
	case diag.Error:
		return "error"
	case diag.Warning:
		return "warning"
	}
	return "note"
}

func subRuleHelp(d diag.Diagnostic) string {
	switch d.RuleID {
	case "ADO701":
		return "https://www.shellcheck.net/wiki/" + d.SubRule
	case "ADO702":
		return "https://learn.microsoft.com/powershell/utility-modules/psscriptanalyzer/rules/" + strings.ToLower(strings.TrimPrefix(d.SubRule, "PS"))
	}
	return ""
}

// SARIF writes a SARIF 2.1.0 log with one run.
func SARIF(w io.Writer, ds []diag.Diagnostic, reg *lint.Registry, opts Options) error {
	uri := opts.URI
	if uri == nil {
		uri = filepath.ToSlash
	}
	driver := sarifDriver{Name: "adolint", Version: opts.Version, InformationURI: toolURI}
	index := map[string]int{}
	for _, r := range reg.All() {
		m := r.Meta()
		index[m.ID] = len(driver.Rules)
		driver.Rules = append(driver.Rules, ruleOf(m.ID, m.Name, m.Summary, m.Description, m.DocsURL(), m.Severity))
	}
	results := []sarifResult{}
	for _, d := range ds {
		id := d.FullID()
		idx, ok := index[id]
		if !ok {
			name, summary, desc, help := id, d.SubRule, "", subRuleHelp(d)
			if parent, found := reg.Get(d.RuleID); found {
				m := parent.Meta()
				name, summary, desc = m.Name+"/"+d.SubRule, m.Summary+" ("+d.SubRule+")", m.Description
			}
			idx = len(driver.Rules)
			index[id] = idx
			driver.Rules = append(driver.Rules, ruleOf(id, name, summary, desc, help, d.Severity))
		}
		region := sarifRegion{StartLine: d.Line, StartColumn: d.Column}
		if d.EndLine > 0 {
			region.EndLine, region.EndColumn = d.EndLine, d.EndColumn
		}
		results = append(results, sarifResult{RuleID: id, RuleIndex: idx, Level: sarifLevel(d.Severity),
			Message: sarifText{d.Message},
			Locations: []sarifLocation{{PhysicalLocation: sarifPhysical{
				ArtifactLocation: sarifArtifact{URI: uri(d.File), URIBaseID: "%SRCROOT%"}, Region: region}}}})
	}
	log := sarifLog{Schema: sarifSchema, Version: "2.1.0", Runs: []sarifRun{{Tool: sarifTool{driver}, Results: results}}}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(log)
}

func ruleOf(id, name, summary, desc, help string, sev diag.Severity) sarifRule {
	r := sarifRule{ID: id, Name: name, ShortDescription: sarifText{summary}, HelpURI: help,
		DefaultConfiguration: sarifRuleConf{Level: sarifLevel(sev)}}
	if desc != "" {
		r.FullDescription = &sarifText{desc}
	}
	return r
}
