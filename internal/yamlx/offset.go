package yamlx

import (
	"strings"

	"go.yaml.in/yaml/v3"
)

// OffsetPos returns the 1-based file line and column of byte offset off inside scalar n's value.
// Literal block scalars map exactly; single-line plain and simply-quoted scalars map exactly;
// other styles fall back to the scalar's own position.
func OffsetPos(src *Source, n *yaml.Node, off int) (line, col int) {
	if n == nil {
		return 1, 1
	}
	if off < 0 || off > len(n.Value) {
		off = 0
	}
	before := n.Value[:off]
	nl := strings.Count(before, "\n")
	lastNL := strings.LastIndexByte(before, '\n')
	switch n.Style {
	case yaml.LiteralStyle:
		indent := BlockIndent(src.Lines, n.Line)
		return n.Line + 1 + nl, indent + 1 + (off - lastNL - 1)
	case yaml.FoldedStyle:
		return n.Line + 1, BlockIndent(src.Lines, n.Line) + 1
	case yaml.SingleQuotedStyle, yaml.DoubleQuotedStyle:
		if nl == 0 && !strings.ContainsAny(n.Value, `'"\`) {
			return n.Line, n.Column + 1 + off
		}
	case 0:
		if !strings.Contains(n.Value, "\n") {
			return n.Line, n.Column + off
		}
	}
	return n.Line, n.Column
}

// BlockIndent returns the indentation of the first non-blank line after a block scalar indicator line (1-based).
func BlockIndent(lines []string, indicatorLine int) int {
	for i := indicatorLine; i < len(lines); i++ {
		l := lines[i]
		if strings.TrimSpace(l) == "" {
			continue
		}
		return len(l) - len(strings.TrimLeft(l, " "))
	}
	return 0
}
