package jinja

import (
	"sort"
	"strings"
)

// A value's type in frame cost keys, an Undefined naming whether it carries a hint, an object or a name
func keyType(v Value) string {
	if u, ok := v.(*Undefined); ok {
		switch {
		case u.hint != "":
			return "Undefined:hint"
		case u.hasObj:
			return "Undefined:obj"
		}
		return "Undefined:name"
	}
	return typeName(v)
}

// A consumed generator's suffix in frame cost keys, the key of the filter call that created it
func producerSuffix(v Value) string {
	if it, ok := v.(*Iterator); ok && it.producer != "" {
		return " |" + it.producer
	}
	return ""
}

// A filter call that created a generator, kept so the calls consuming it are costed from its source and arguments
type filterCall struct {
	name string
	v    Value
	args []Value
	kw   *Dict
	// Items already pulled from a generator source when the call was made
	skip int
}

// Filters whose first positional argument names a filter, test or attribute their call goes through
var namedArgFilters = map[string]bool{"map": true, "select": true, "reject": true, "selectattr": true, "rejectattr": true}

// The frame cost key of a filter or test call, a consumed generator adding its producing call's key
func builtinCallKey(name string, isFilter bool, v Value, args []Value, kw *Dict) string {
	kind := "test "
	if isFilter {
		kind = "filter "
	}
	names := make([]string, len(kw.keys))
	for i, k := range kw.keys {
		names[i] = k.(string)
	}
	sort.Strings(names)
	key := kind + name + " " + keyType(v) + " " + itoa(len(args)) + " " + strings.Join(names, ",")
	if s, ok := firstArg(args).(string); ok && isFilter && namedArgFilters[name] {
		key += " " + s
	}
	return key + producerSuffix(v)
}

func firstArg(args []Value) Value {
	if len(args) == 0 {
		return nil
	}
	return args[0]
}

// Frames a builtin filter or test call pushes above its caller
func (r *renderer) builtinCallFrames(name string, isFilter bool, v Value, args []Value, kw *Dict) int {
	key := builtinCallKey(name, isFilter, v, args, kw)
	return r.opFrames(key, v, args, kw)
}

// The modeled operation a key belongs to, its first one or two words, empty when the table prices it
func modeledOp(key string) string {
	first, rest, _ := strings.Cut(key, " ")
	if warmModeled[first] {
		return first
	}
	second, _, _ := strings.Cut(rest, " ")
	if op := first + " " + second; warmModeled[op] {
		return op
	}
	return ""
}

// Frames a measured operation pushes above its caller, by its Python call path port when modeled, else the table
func (r *renderer) opFrames(key string, v Value, args []Value, kw *Dict) int {
	if op := modeledOp(key); op != "" {
		if kw == nil {
			kw = newDict()
		}
		return frameModels[op](r, key, v, args, kw)
	}
	return warmBuiltinFrames(key, v)
}

// Records an operation's frames under its key while costs are tracked, failing past the recursion limit
func (r *renderer) measured(key string, k int) error {
	if r.costs != nil {
		r.costs[key] = max(r.costs[key], k)
	}
	return r.transient(k)
}

// The frame cost key of SandboxedEnvironment.getattr, keyed by object type and the path the lookup takes
func (r *renderer) getattrKey(obj Value, attr string) string {
	return "getattr " + keyType(obj) + " " + r.getattrOutcome(obj, attr)
}

// Frames SandboxedEnvironment.getattr pushes above its caller
func (r *renderer) getattrFrames(obj Value, attr string) int {
	return r.opFrames(r.getattrKey(obj, attr), obj, nil, nil)
}

// The path Python's getattr then item access take, raise for an undefined value's non dunder names
func (r *renderer) getattrOutcome(obj Value, attr string) string {
	if _, ok := obj.(*Undefined); ok {
		switch {
		case hasPrivateAttr(obj, attr):
			return "private"
		case strings.HasPrefix(attr, "__"):
			return "missing"
		}
		return "raise"
	}
	if _, found, err := r.attribute(obj, attr); err == nil && found {
		return attrOutcome(obj, attr)
	}
	if _, found, err := itemOf(obj, attr); err == nil && found {
		return "item"
	}
	return "missing"
}

// The path the sandbox takes for a found attribute, a wrapped str format, private, mutating or safe
func attrOutcome(obj Value, attr string) string {
	switch obj.(type) {
	case string, Markup:
		if attr == "format" || attr == "format_map" {
			return "format"
		}
	}
	switch {
	case strings.HasPrefix(attr, "_"):
		return "private"
	case unsafeAttr(obj, attr):
		return "mutable"
	}
	return "attr"
}

// The frame cost key of SandboxedEnvironment.getitem, empty for slices jinja2 subscripts natively
func (r *renderer) getitemKey(obj, key Value) string {
	if _, ok := key.(*sliceValue); ok {
		return ""
	}
	outcome := "missing"
	s, isStr := key.(string)
	if _, ok := obj.(*Undefined); ok {
		if isStr && hasPrivateAttr(obj, s) {
			outcome = "private"
		}
	} else if _, found, err := itemOf(obj, key); err == nil && found {
		outcome = "item"
	} else if isStr {
		if _, found, err := r.attribute(obj, s); err == nil && found {
			outcome = attrOutcome(obj, s)
		}
	}
	return "getitem " + keyType(obj) + " " + keyType(key) + " " + outcome
}

// Frames SandboxedEnvironment.getitem pushes above its caller
func (r *renderer) getitemFrames(obj, key Value) int {
	k := r.getitemKey(obj, key)
	if k == "" {
		return 0
	}
	return r.opFrames(k, obj, nil, nil)
}

// The frame cost key of SandboxedEnvironment.call, empty for macros, loops and blocks whose frames the renderer enters
func callKey(fn Value) string {
	switch f := fn.(type) {
	case *Macro, *LoopContext, *BlockRef:
		return ""
	case *Builtin:
		owner := "-"
		if f.self != nil {
			owner = typeName(f.self)
		}
		_, isStr := asStr(f.self)
		_, isDict := f.self.(*Dict)
		switch {
		case isStr && (f.name == "format" || f.name == "format_map"):
			owner = "-"
		case isStr && f.name == "maketrans":
			owner = "-"
		case isDict && f.name == "fromkeys":
			owner = "type"
		}
		return "call " + f.name[strings.LastIndex(f.name, ".")+1:] + " " + owner
	}
	return "call " + keyType(fn) + " -"
}

// The object a call's frame cost is measured on, a bound method's receiver
func callSubject(fn Value) Value {
	if f, ok := fn.(*Builtin); ok {
		return f.self
	}
	return nil
}

// Frames markupsafe's escape pushes for a value
func (r *renderer) markupEscapeFrames(v Value) int {
	key := "escape " + keyType(v)
	return r.opFrames(key, v, nil, nil)
}

// Fails unless k frames above the running Python frame fit under the recursion limit, recording the deepest frame reached
func (r *renderer) transient(k int) error {
	if r.frames+k > recursionLimit && !r.probing {
		return recursionError()
	}
	r.peak = max(r.peak, r.frames+k)
	return nil
}
