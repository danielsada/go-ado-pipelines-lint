package template

import (
	"go.yaml.in/yaml/v3"

	"github.com/danielsada/go-ado-pipelines-lint/internal/yamlx"
)

type mappingChoice struct {
	bodies []*yaml.Node
}

func (e *expander) recordChoice(out, body *yaml.Node, ch *chain, conditional bool) {
	if !conditional || !yamlx.IsMap(body) {
		return
	}
	if ch.choice == nil {
		ch.choice = &mappingChoice{}
		e.choices[out] = append(e.choices[out], ch.choice)
	}
	ch.choice.bodies = append(ch.choice.bodies, body)
}

func (e *expander) chargeTree(n *yaml.Node, f *frame) {
	// Each alternate view is linted as a full document, including its shared subtrees.
	e.consume(n, f)
	for _, child := range n.Content {
		e.chargeTree(child, f)
	}
}

// forVariants varies one independent mapping choice at a time, including choices nested
// in alternate branches. It does not construct the Cartesian product of unrelated choices.
func (e *expander) forVariants(n *yaml.Node, f *frame, emit func(*yaml.Node)) {
	if n == nil || e.exhausted {
		return
	}
	e.consume(n, f)
	emit(n)
	for _, choice := range e.choices[n] {
		for _, body := range choice.bodies {
			if e.exhausted {
				return
			}
			alt := e.mappingVariant(n, choice, body, f)
			e.forVariants(alt, f, emit)
		}
	}
	e.childVariants(n, f, emit)
}

func (e *expander) childVariants(n *yaml.Node, f *frame, emit func(*yaml.Node)) {
	for i, child := range n.Content {
		e.forVariants(child, f, func(alt *yaml.Node) {
			if alt == child {
				return
			}
			parent := e.shallowClone(n, f)
			parent.Content[i] = alt
			emit(parent)
		})
	}
}

func (e *expander) shallowClone(n *yaml.Node, f *frame) *yaml.Node {
	e.consumeWork(len(n.Content), n, f)
	out := e.newNode(n.Kind, n.Tag, n, f)
	out.Value, out.Style = n.Value, n.Style
	out.Content = append([]*yaml.Node(nil), n.Content...)
	if e.doc.Open(n) {
		e.doc.MarkOpen(out)
	}
	if e.doc.Conditional(n) {
		e.doc.MarkConditional(out)
	}
	return out
}

func (e *expander) mappingVariant(n *yaml.Node, choice *mappingChoice, body *yaml.Node, f *frame) *yaml.Node {
	keys := map[string]bool{}
	for _, branch := range choice.bodies {
		yamlx.Pairs(branch, func(k, _ *yaml.Node) { keys[k.Value] = true })
	}
	out := e.shallowClone(n, f)
	out.Content = nil
	yamlx.Pairs(n, func(k, v *yaml.Node) {
		if !keys[k.Value] {
			out.Content = append(out.Content, k, v)
		}
	})
	out.Content = append(out.Content, body.Content...)
	e.choices[out] = append(e.choices[out], e.choices[body]...)
	return out
}
