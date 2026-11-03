package jinja

import (
	"fmt"
	"strings"
)

// jinja2's TemplateReference, the self a template body reads, reaching each of its blocks by name
type TemplateRef struct{ blocks map[string]*blockStmt }

// jinja2's BlockReference, a block reached through self that renders it when called
type BlockRef struct {
	name  string
	block *blockStmt
}

// An assignment jinja2's root and block functions open with, self to the template reference and super to the parent block
type bindStmt struct {
	name  string
	value Value
}

// TemplateReference.__getitem__, the block of that name or the KeyError the context's block dict raises
func (t *TemplateRef) item(key Value) (Value, bool) {
	if name, ok := key.(string); ok {
		if b, ok := t.blocks[name]; ok {
			return &BlockRef{name: name, block: b}, true
		}
	}
	return nil, false
}

// BlockReference attributes, its name and super, which has no parent block without template inheritance
func blockRefAttr(b *BlockRef, name string) (Value, bool) {
	switch name {
	case "name":
		return b.name, true
	case "super":
		return &Undefined{hint: "there is no parent block called " + strRepr(b.name) + ".", name: "super"}, true
	}
	return nil, false
}

// Frames from the caller to a called block's body, the sandbox's call, Context.call, BlockReference.__call__ and the block function
const blockRefCallFrames = 4

// BlockReference.__call__, rendering the block over the template context and marking it safe under autoescape
func (r *renderer) callBlockRef(b *BlockRef, args []Value, kw *Dict) (Value, error) {
	if len(kw.keys) > 0 {
		return nil, typeError("BlockReference.__call__() got an unexpected keyword argument %s", strRepr(kw.keys[0].(string)))
	}
	if len(args) > 0 {
		return nil, typeError("BlockReference.__call__() takes 1 positional argument but %d were given", len(args)+1)
	}
	if err := r.enter(blockRefCallFrames); err != nil {
		return nil, err
	}
	defer r.leave(blockRefCallFrames)
	saved := r.autoescape
	r.autoescape = false
	var buf strings.Builder
	err := r.exec(b.block.body, newScope(r.root), &buf)
	r.autoescape = saved
	if err != nil {
		return nil, err
	}
	return r.wrapRendered(buf.String()), nil
}

// Whether a value is Markup, as output text jinja2 folded at compile time is
func isMarkup(v Value) bool {
	_, ok := v.(Markup)
	return ok
}

func (b *BlockRef) repr() string {
	return fmt.Sprintf("<jinja2.runtime.BlockReference object at %p>", b)
}
