package expr

import (
	"testing"
)

func mustParse(t *testing.T, s string) Node {
	t.Helper()
	n, err := Parse(s)
	if err != nil {
		t.Fatalf("Parse(%q): %v", s, err)
	}
	return n
}

func TestParseErrors(t *testing.T) {
	bad := []string{"", "eq(", "eq(a,", "'unterminated", "a.", "a[1", "eq(a b)", "a $ b", "1.x.2"}
	for _, s := range bad {
		if _, err := Parse(s); err == nil {
			t.Errorf("Parse(%q): expected error", s)
		}
	}
}

func TestFormatRoundTrip(t *testing.T) {
	cases := map[string]string{
		"eq(variables['Build.Reason'], 'PullRequest')": "eq(variables['Build.Reason'], 'PullRequest')",
		"and(succeeded(),ne(parameters.x,'It''s'))":    "and(succeeded(), ne(parameters.x, 'It''s'))",
		"split(x, '/')[8]":                             "split(x, '/')[8]",
		"dependencies.A.outputs['s.v']":                "dependencies.A.outputs['s.v']",
		"True":                                         "True",
		"1.2.3":                                        "1.2.3",
	}
	for in, want := range cases {
		if got := Format(mustParse(t, in)); got != want {
			t.Errorf("Format(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEval(t *testing.T) {
	ctx := NewContext()
	ctx.Set("parameters", FromObj(map[string]Value{"env": Str("prod"), "flag": Bool(true), "n": Num(3)}))
	cases := []struct {
		expr string
		want string // "T", "F" or "?" (unknown), or a string value
	}{
		{"true", "T"},
		{"eq('a', 'A')", "T"},
		{"eq('true', true)", "T"},
		{"eq('false', true)", "F"},
		{"ne(1, 2)", "T"},
		{"not(eq(1, 2))", "T"},
		{"and(succeeded(), false)", "F"},
		{"and(succeeded(), true)", "?"},
		{"or(variables.x, true)", "T"},
		{"or(variables.x, false)", "?"},
		{"always()", "?"},
		{"eq(parameters.env, 'prod')", "T"},
		{"eq(parameters['env'], 'dev')", "F"},
		{"parameters.flag", "T"},
		{"gt(parameters.n, 2)", "T"},
		{"in('B', 'A', 'B', 'C')", "T"},
		{"notIn('D', 'A', 'B', 'C')", "T"},
		{"xor(true, false)", "T"},
		{"contains('ABCDE', 'bcd')", "T"},
		{"startsWith('ABCDE', 'ab')", "T"},
		{"endsWith('ABCDE', 'de')", "T"},
		{"eq(variables['Build.SourceBranch'], 'refs/heads/main')", "?"},
		{"format('{0} Build', parameters.env)", "prod Build"},
		{"coalesce('', parameters.env)", "prod"},
		{"lower('FOO')", "foo"},
		{"replace('a-b', '-', '_')", "a_b"},
		{"join(';', split('a,b', ','))", "a;b"},
		{"iif(eq(1,1), 'x', 'y')", "x"},
		{"length('fabrikam')", "8"},
		{"lt(1.2.3, 1.10.0)", "T"},
		{"eq(parameters.missing, '')", "T"},
	}
	for _, c := range cases {
		v := Eval(mustParse(t, c.expr), ctx)
		got := "?"
		switch {
		case !v.Known():
		case v.Kind == KBool:
			got = map[bool]string{true: "T", false: "F"}[v.B]
		default:
			got = v.String()
		}
		if got != c.want {
			t.Errorf("Eval(%q) = %s, want %s", c.expr, got, c.want)
		}
	}
}

func FromObj(m map[string]Value) Value {
	v := Value{Kind: KObject}
	for k, val := range m {
		v.Obj = append(v.Obj, KV{Key: k, Val: val})
	}
	return v
}

func TestCheckCalls(t *testing.T) {
	cases := map[string]int{
		"eq(a, b)":             0,
		"eqq(a, b)":            1,
		"eq(a)":                1,
		"and(a)":               1,
		"succeeded('A', 'B')":  0,
		"always(1)":            1,
		"Eq(A, b)":             0,
		"and(eq(a,b), foo())":  1,
		"not(not(not(1, 2)))":  1,
		"coalesce(a, b, c, d)": 0,
	}
	for s, want := range cases {
		if got := len(CheckCalls(mustParse(t, s))); got != want {
			t.Errorf("CheckCalls(%q) = %d errors, want %d", s, got, want)
		}
	}
}

func TestScan(t *testing.T) {
	spans, errs := Scan("echo ${{ parameters.x }} $(Build.Id) $[ variables['a]'] ] $(pwd) done")
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	kinds := []SpanKind{SpanTemplate, SpanMacro, SpanRuntime, SpanMacro}
	if len(spans) != len(kinds) {
		t.Fatalf("got %d spans, want %d: %+v", len(spans), len(kinds), spans)
	}
	for i, k := range kinds {
		if spans[i].Kind != k {
			t.Errorf("span %d kind = %v, want %v", i, spans[i].Kind, k)
		}
	}
	if spans[0].Expr != " parameters.x " || spans[1].Expr != "Build.Id" {
		t.Errorf("unexpected span text: %q %q", spans[0].Expr, spans[1].Expr)
	}
	if _, errs := Scan("${{ parameters.x "); len(errs) != 1 {
		t.Errorf("expected unterminated error")
	}
	if _, errs := Scan("$[ eq(a, b) "); len(errs) != 1 {
		t.Errorf("expected unterminated runtime error")
	}
	if _, errs := Scan("${{ '}}' }}"); len(errs) != 0 {
		t.Errorf("quoted '}}' should not close the expression")
	}
}

func TestParseDirective(t *testing.T) {
	cases := []struct {
		key     string
		kind    DirectiveKind
		wantErr bool
		isDir   bool
	}{
		{"${{ if eq(parameters.a, 'b') }}", DirIf, false, true},
		{"${{ elseif true }}", DirElseIf, false, true},
		{"${{ else }}", DirElse, false, true},
		{"${{ each s in parameters.steps }}", DirEach, false, true},
		{"${{ insert }}", DirInsert, false, true},
		{"${{ parameters.key }}", DirExpr, false, true},
		{"${{ if }}", DirIf, true, true},
		{"${{ each in x }}", DirEach, true, true},
		{"${{ else true }}", DirElse, true, true},
		{"plain", 0, false, false},
		{"prefix ${{ x }}", 0, false, false},
	}
	for _, c := range cases {
		d, err := ParseDirective(c.key)
		if (d != nil) != c.isDir {
			t.Errorf("%q: directive = %v, want %v", c.key, d != nil, c.isDir)
			continue
		}
		if (err != nil) != c.wantErr {
			t.Errorf("%q: err = %v, wantErr %v", c.key, err, c.wantErr)
		}
		if d != nil && d.Kind != c.kind {
			t.Errorf("%q: kind = %v, want %v", c.key, d.Kind, c.kind)
		}
	}
	d, _ := ParseDirective("${{ each s in parameters.steps }}")
	if d.Var != "s" || Format(d.Coll) != "parameters.steps" {
		t.Errorf("each parsed as var=%q coll=%q", d.Var, Format(d.Coll))
	}
}

func TestPath(t *testing.T) {
	p, ok := Path(mustParse(t, "parameters.a['b'].c"))
	if !ok || len(p) != 4 || p[2] != "b" {
		t.Errorf("Path = %v, %v", p, ok)
	}
	if _, ok := Path(mustParse(t, "eq(a, b)")); ok {
		t.Errorf("Path of call should not be ok")
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{"eq(a, 'b')", "and(succeeded(), ne(variables['x'], 1.2.3))", "a.b[0].c", "'it''s'", "-1.5"} {
		f.Add(s)
	}
	f.Fuzz(func(_ *testing.T, s string) {
		n, err := Parse(s)
		if err != nil {
			return
		}
		_ = Eval(n, NewContext())
		_ = CheckCalls(n)
		if _, err := Parse(Format(n)); err != nil {
			panic("formatted expression does not re-parse: " + Format(n))
		}
	})
}

func FuzzScan(f *testing.F) {
	for _, s := range []string{"${{ x }}", "$[ a[b] ]", "$(a.b) ${{ '}}' }}", "${{ if x }}"} {
		f.Add(s)
	}
	f.Fuzz(func(_ *testing.T, s string) {
		spans, _ := Scan(s)
		for _, sp := range spans {
			if sp.Start < 0 || sp.End > len(s) || sp.Start >= sp.End {
				panic("invalid span")
			}
		}
		_, _ = ParseDirective(s)
	})
}
