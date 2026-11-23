package yamlx

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestParseDiagnostics(t *testing.T) {
	src, ds := Parse("a.yml", "", []byte("a: 1\nb:\n  c: 1\n  c: 2\nlist:\n- x: 1\n  x: 2\n"))
	if src.Invalid || len(ds) != 2 {
		t.Fatalf("got %v", ds)
	}
	if ds[0].RuleID != RuleDuplicateKey || ds[0].Line != 4 || !strings.Contains(ds[0].Message, "line 3") {
		t.Errorf("first duplicate = %+v", ds[0])
	}
	_, ds = Parse("b.yml", "", []byte("a: b: c\n"))
	if len(ds) != 1 || ds[0].RuleID != RuleSyntax || ds[0].Line != 1 {
		t.Errorf("syntax error = %+v", ds)
	}
	src, ds = Parse("c.yml", "", []byte("\xef\xbb\xbfa: 1\r\nb: 2\r\n"))
	if len(ds) != 0 || Str(src.Root, "b") != "2" {
		t.Errorf("BOM/CRLF handling: %v %v", ds, src.Root)
	}
}

func TestOffsetPos(t *testing.T) {
	text := "plain: echo ${{ x }}\nquoted: 'echo ${{ x }}'\nblock: |\n  first\n  echo ${{ x }}\n"
	src, _ := Parse("a.yml", "", []byte(text))
	cases := []struct {
		key        string
		line, col  int
		valueIndex string
	}{
		{"plain", 1, 13, "${{"},
		{"quoted", 2, 15, "${{"},
		{"block", 5, 8, "${{"},
	}
	for _, c := range cases {
		n := Value(src.Root, c.key)
		line, col := OffsetPos(src, n, strings.Index(n.Value, c.valueIndex))
		if line != c.line || col != c.col {
			t.Errorf("%s: got %d:%d, want %d:%d", c.key, line, col, c.line, c.col)
		}
	}
}

func TestDocOrigins(t *testing.T) {
	src, _ := Parse("main.yml", "", []byte("a: 1\n"))
	other, _ := Parse("tpl.yml", "", []byte("\n\nb: 2\n"))
	d := NewDoc(src)
	orig := Value(other.Root, "b")
	clone := &yaml.Node{Kind: yaml.ScalarNode, Value: "2"}
	d.SetOrigin(clone, other, orig)
	clone2 := &yaml.Node{Kind: yaml.ScalarNode, Value: "2"}
	d.SetOrigin(clone2, src, clone)
	if l := d.Loc(clone2); l.File != "tpl.yml" || l.Line != 3 || l.Column != 4 {
		t.Errorf("Loc = %+v", l)
	}
	if l := d.Loc(Value(src.Root, "a")); l.File != "main.yml" || l.Line != 1 {
		t.Errorf("Loc(main) = %+v", l)
	}
}
