package yamlx

import (
	"go.yaml.in/yaml/v3"

	"github.com/danielsada/go-ado-pipelines-lint/internal/diag"
)

// Origin identifies where an (expanded) node came from.
type Origin struct {
	Src  *Source
	Node *yaml.Node
}

// Doc is a YAML tree, possibly assembled from several sources by template expansion.
// It records the origin of every node so diagnostics point at the original file and position.
type Doc struct {
	Root *yaml.Node
	// Main is the entry source; nodes without a recorded origin belong to it.
	Main        *Source
	origin      map[*yaml.Node]Origin
	conditional map[*yaml.Node]bool
	open        map[*yaml.Node]bool
	// Sources lists every source file that contributed to the document.
	Sources []*Source
}

// NewDoc wraps a single source as a document.
func NewDoc(src *Source) *Doc {
	return &Doc{
		Root:        src.Root,
		Main:        src,
		origin:      map[*yaml.Node]Origin{},
		conditional: map[*yaml.Node]bool{},
		open:        map[*yaml.Node]bool{},
		Sources:     []*Source{src},
	}
}

// WithRoot returns a view of the document with a different root that shares its origin data.
func (d *Doc) WithRoot(root *yaml.Node) *Doc {
	c := *d
	c.Root = root
	return &c
}

// SetOrigin records that n was produced from orig in src.
func (d *Doc) SetOrigin(n *yaml.Node, src *Source, orig *yaml.Node) {
	if o, ok := d.origin[orig]; ok {
		d.origin[n] = o
		return
	}
	d.origin[n] = Origin{Src: src, Node: orig}
}

// OriginOf returns the origin of n.
func (d *Doc) OriginOf(n *yaml.Node) Origin {
	if o, ok := d.origin[n]; ok {
		return o
	}
	return Origin{Src: d.Main, Node: n}
}

// HasOrigin reports whether an origin was recorded for n.
func (d *Doc) HasOrigin(n *yaml.Node) bool {
	_, ok := d.origin[n]
	return ok
}

// Loc returns the diagnostic location of n.
func (d *Doc) Loc(n *yaml.Node) diag.Location {
	if n == nil {
		return diag.Location{File: d.Main.Path, Line: 1, Column: 1}
	}
	o := d.OriginOf(n)
	l := diag.Location{File: o.Src.Path, Line: o.Node.Line, Column: o.Node.Column}
	if l.Line == 0 {
		l.Line, l.Column = 1, 1
	}
	if o.Node.Kind == yaml.ScalarNode && o.Node.Style == 0 && !containsNewline(o.Node.Value) {
		l.EndLine, l.EndColumn = l.Line, l.Column+len(o.Node.Value)
	}
	return l
}

func containsNewline(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return true
		}
	}
	return false
}

// MarkConditional records that n came from a template branch whose condition could not be
// evaluated, so it may not be present at run time.
func (d *Doc) MarkConditional(n *yaml.Node) { d.conditional[n] = true }

// Conditional reports whether n was produced under an unresolved condition.
func (d *Doc) Conditional(n *yaml.Node) bool { return d.conditional[n] }

// MarkOpen records that collection n had content that could not be resolved
// (for example a remote template), so it may contain more items than are visible.
func (d *Doc) MarkOpen(n *yaml.Node) { d.open[n] = true }

// Open reports whether collection n may contain unresolved content.
func (d *Doc) Open(n *yaml.Node) bool { return d.open[n] }

// AddSource registers a contributing source (no duplicates).
func (d *Doc) AddSource(src *Source) {
	for _, s := range d.Sources {
		if s == src {
			return
		}
	}
	d.Sources = append(d.Sources, src)
}
