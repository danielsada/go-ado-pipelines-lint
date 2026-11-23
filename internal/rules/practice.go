package rules

import (
	"fmt"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/danielsada/go-ado-pipelines-lint/internal/expr"
	"github.com/danielsada/go-ado-pipelines-lint/internal/lint"
	"github.com/danielsada/go-ado-pipelines-lint/internal/model"
	"github.com/danielsada/go-ado-pipelines-lint/internal/yamlx"
)

func firstKey(root *yaml.Node) *yaml.Node {
	if yamlx.IsMap(root) && len(root.Content) > 0 {
		return root.Content[0]
	}
	return root
}

func checkImplicitCI(c *lint.PipelineContext) {
	if c.Pipeline.IsTemplate || !yamlx.IsMap(c.Doc.Root) || yamlx.Has(c.Doc.Root, "trigger") {
		return
	}
	c.Report("ADO401", firstKey(c.Doc.Root), "no trigger is defined, so CI runs for pushes to every branch; set trigger: none or add a branch filter")
}

func checkImplicitPR(c *lint.PipelineContext) {
	if c.Pipeline.IsTemplate || !yamlx.IsMap(c.Doc.Root) || yamlx.Has(c.Doc.Root, "pr") {
		return
	}
	c.Report("ADO402", firstKey(c.Doc.Root), "no pr trigger is defined, so pull request validation runs for every branch (GitHub and Bitbucket repositories); set pr: none or add a branch filter")
}

var (
	secretNameRe   = regexp.MustCompile(`(?i)(password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|connection[_-]?string|client[_-]?secret|credential)`)
	secretExceptRe = regexp.MustCompile(`(?i)(name|names|url|uri|file|path|id|type|version|enabled|length|count|scope|endpoint|connection|header|location|kind|mode|policy)$`)
	nonSecretRe    = regexp.MustCompile(`(?i)^(true|false|yes|no|on|off|none|null|\d{1,3})$`)
)

func looksSecret(name string, value *yaml.Node) bool {
	if !secretNameRe.MatchString(name) || secretExceptRe.MatchString(name) || !yamlx.IsScalar(value) {
		return false
	}
	v := strings.TrimSpace(value.Value)
	return v != "" && !expr.HasDynamic(v) && !nonSecretRe.MatchString(v)
}

func checkHardcodedSecrets(c *lint.PipelineContext) {
	p := c.Pipeline
	check := func(vs []*model.Variable) {
		for _, v := range vs {
			if looksSecret(v.Name, v.Value) {
				c.Report("ADO403", v.Value, fmt.Sprintf("variable %q looks like a secret but has a literal value in YAML; use a secret variable, a variable group or Azure Key Vault instead", v.Name))
			}
		}
	}
	check(p.Variables)
	for _, s := range p.Stages {
		check(s.Variables)
		for _, j := range s.Jobs {
			check(j.Variables)
			for _, st := range j.Steps {
				yamlx.Pairs(st.Env, func(k, v *yaml.Node) {
					if looksSecret(k.Value, v) {
						c.Report("ADO403", v, fmt.Sprintf("environment variable %q looks like a secret but has a literal value; map a secret variable instead, e.g. %s: $(%s)", k.Value, k.Value, k.Value))
					}
				})
			}
		}
	}
}

// RetiredImages lists Microsoft-hosted image labels that have been retired or are deprecated,
// per https://learn.microsoft.com/azure/devops/pipelines/agents/hosted and its deprecation schedule.
var RetiredImages = map[string]string{
	"windows-2019":     "was retired on December 31, 2025; use windows-2022 or windows-latest",
	"vs2017-win2016":   "has been retired; use windows-2022 or windows-latest",
	"vs2015-win2012r2": "has been retired; use windows-2022 or windows-latest",
	"win1803":          "has been retired; use windows-2022 or windows-latest",
	"ubuntu-16.04":     "has been retired; use ubuntu-24.04 or ubuntu-latest",
	"ubuntu-18.04":     "has been retired; use ubuntu-24.04 or ubuntu-latest",
	"ubuntu-20.04":     "has been retired; use ubuntu-24.04 or ubuntu-latest",
	"macos-10.13":      "has been retired; use macOS-15 or macOS-latest",
	"macos-10.14":      "has been retired; use macOS-15 or macOS-latest",
	"macos-10.15":      "has been retired; use macOS-15 or macOS-latest",
	"macos-11":         "has been retired; use macOS-15 or macOS-latest",
	"macos-12":         "has been retired; use macOS-15 or macOS-latest",
	"macos-13":         "was retired on December 4, 2025; use macOS-15 or macOS-latest",
	"ubuntu-22.04":     "is deprecated and scheduled for removal on April 17, 2027; use ubuntu-24.04 or ubuntu-latest",
	"macos-14":         "is deprecated (deprecation started July 6, 2026); use macOS-15 or macOS-latest",
}

func checkRetiredImages(c *lint.PipelineContext) {
	p := c.Pipeline
	seen := map[*yaml.Node]bool{}
	check := func(pool *model.Pool) {
		if pool == nil || pool.VMImageNode == nil || seen[pool.VMImageNode] {
			return
		}
		seen[pool.VMImageNode] = true
		if why, ok := RetiredImages[strings.ToLower(strings.TrimSpace(pool.VMImage))]; ok {
			c.Report("ADO404", pool.VMImageNode, fmt.Sprintf("vmImage %q %s", pool.VMImage, why))
		}
	}
	check(p.Pool)
	for _, s := range p.Stages {
		check(s.Pool)
		for _, j := range s.Jobs {
			check(j.Pool)
		}
	}
}

func checkContinueOnError(c *lint.PipelineContext) {
	report := func(unit *yaml.Node, what string) {
		if k, v := yamlx.Get(unit, "continueOnError"); k != nil && yamlx.IsScalar(v) && strings.EqualFold(strings.TrimSpace(v.Value), "true") {
			c.Report("ADO405", v, fmt.Sprintf("continueOnError: true lets the %s fail without failing the run (the run is reported as partially succeeded)", what))
		}
	}
	for _, j := range c.Pipeline.AllJobs() {
		if !j.Implicit {
			report(j.Node, "job")
		}
		for _, st := range j.Steps {
			report(st.Node, "step")
		}
	}
}
