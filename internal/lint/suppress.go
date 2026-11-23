package lint

import (
	"regexp"
	"strings"

	"github.com/danielsada/go-ado-pipelines-lint/internal/diag"
	"github.com/danielsada/go-ado-pipelines-lint/internal/yamlx"
)

var suppressRe = regexp.MustCompile(`#\s*adolint:(disable-next-line|disable-line|disable-file)\b([^#]*)`)

// suppressions holds inline suppressions for one file. A nil set means "all rules".
type suppressions struct {
	lines map[int]idSet
	file  idSet
	all   bool
}

type idSet map[string]bool

func (s idSet) matches(d diag.Diagnostic) bool {
	return s == nil || s[d.RuleID] || s[d.FullID()]
}

func parseIDs(s string) idSet {
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
	if len(fields) == 0 {
		return nil
	}
	set := idSet{}
	for _, f := range fields {
		set[strings.ToUpper(f)] = true
	}
	return set
}

func merge(a, b idSet, aSet bool) idSet {
	if !aSet || a == nil || b == nil {
		if !aSet {
			return b
		}
		return nil
	}
	for k := range b {
		a[k] = true
	}
	return a
}

// parseSuppressions scans a source's lines for adolint comments.
func parseSuppressions(src *yamlx.Source) *suppressions {
	s := &suppressions{lines: map[int]idSet{}}
	for i, line := range src.Lines {
		m := suppressRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		ids := parseIDs(m[2])
		switch m[1] {
		case "disable-file":
			s.file = merge(s.file, ids, s.all)
			s.all = true
		case "disable-line":
			s.add(i+1, ids)
		case "disable-next-line":
			s.add(nextCodeLine(src.Lines, i+1), ids)
		}
	}
	return s
}

func (s *suppressions) add(line int, ids idSet) {
	cur, ok := s.lines[line]
	s.lines[line] = merge(cur, ids, ok)
}

// nextCodeLine returns the 1-based number of the first non-blank, non-comment line after index i.
func nextCodeLine(lines []string, i int) int {
	for ; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if t != "" && !strings.HasPrefix(t, "#") {
			return i + 1
		}
	}
	return i + 1
}

func (s *suppressions) suppressed(d diag.Diagnostic) bool {
	if s == nil {
		return false
	}
	if s.all && s.file.matches(d) {
		return true
	}
	ids, ok := s.lines[d.Line]
	return ok && ids.matches(d)
}
