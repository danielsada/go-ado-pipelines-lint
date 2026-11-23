package lint

import (
	"testing"

	"github.com/danielsada/go-ado-pipelines-lint/internal/diag"
	"github.com/danielsada/go-ado-pipelines-lint/internal/yamlx"
)

func TestSubRuleSuppressionIsCaseInsensitive(t *testing.T) {
	src, _ := yamlx.Parse("a.yml", "", []byte("steps:\n- pwsh: Invoke-Expression x # adolint:disable-line ADO702/PSAvoidUsingInvokeExpression\n"))
	d := diag.Diagnostic{RuleID: "ADO702", SubRule: "PSAvoidUsingInvokeExpression", Location: diag.Location{File: "a.yml", Line: 2}}
	if !parseSuppressions(src).suppressed(d) {
		t.Error("mixed-case sub-rule suppression did not match")
	}
	d.SubRule = "PSAvoidUsingWriteHost"
	if parseSuppressions(src).suppressed(d) {
		t.Error("suppression matched a different sub-rule")
	}
}
