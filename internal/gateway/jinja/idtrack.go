package jinja

import "sort"

// Load instructions jinja2's idtracking records per reference
const (
	loadParameter = "param"
	loadResolve   = "resolve"
	loadAlias     = "alias"
	loadUndefined = "undefined"
)

// A load instruction and its parameter, the resolved name or the aliased reference
type symLoad struct{ action, param string }

// jinja2's Symbols, references per scope level with loads kept in insertion order as Python dicts keep them
type symbols struct {
	level     int
	parent    *symbols
	refs      map[string]string
	loadKeys  []string
	loads     map[string]symLoad
	stores    map[string]bool
	storeKeys []string
}

// Symbols.__init__ frame included, a scope at the given level or one below the parent
func (g *codegen) newSymbols(parent *symbols, level int, hasLevel bool) *symbols {
	g.need(1)
	if !hasLevel {
		level = 0
		if parent != nil {
			level = parent.level + 1
		}
	}
	return &symbols{level: level, parent: parent, refs: map[string]string{}, loads: map[string]symLoad{}, stores: map[string]bool{}}
}

func (s *symbols) setLoad(ident string, l symLoad) {
	if _, ok := s.loads[ident]; !ok {
		s.loadKeys = append(s.loadKeys, ident)
	}
	s.loads[ident] = l
}

func (s *symbols) addStore(name string) {
	if !s.stores[name] {
		s.stores[name] = true
		s.storeKeys = append(s.storeKeys, name)
	}
}

func (g *codegen) defineRef(s *symbols, name string, load *symLoad) string {
	g.enter()
	defer g.leave()
	ident := "l_" + itoa(s.level) + "_" + name
	s.refs[name] = ident
	if load != nil {
		s.setLoad(ident, *load)
	}
	return ident
}

func (g *codegen) findLoad(s *symbols, target string) (symLoad, bool) {
	g.enter()
	defer g.leave()
	if l, ok := s.loads[target]; ok {
		return l, true
	}
	if s.parent != nil {
		return g.findLoad(s.parent, target)
	}
	return symLoad{}, false
}

func (g *codegen) findRef(s *symbols, name string) (string, bool) {
	g.enter()
	defer g.leave()
	if r, ok := s.refs[name]; ok {
		return r, true
	}
	if s.parent != nil {
		return g.findRef(s.parent, name)
	}
	return "", false
}

// Symbols.ref, failing with jinja2's AssertionError for a name the frame never saw
func (g *codegen) ref(s *symbols, name string) string {
	g.enter()
	defer g.leave()
	r, ok := g.findRef(s, name)
	if !ok {
		g.raise(&Error{Kind: "AssertionError", Msg: "Tried to resolve a name to a reference that was unknown to the frame (" + strRepr(name) + ")"})
	}
	return r
}

func (g *codegen) copySymbols(s *symbols) *symbols {
	g.enter()
	defer g.leave()
	c := *s
	c.refs = make(map[string]string, len(s.refs))
	for k, v := range s.refs {
		c.refs[k] = v
	}
	c.loads = make(map[string]symLoad, len(s.loads))
	for k, v := range s.loads {
		c.loads[k] = v
	}
	c.loadKeys = append([]string(nil), s.loadKeys...)
	c.stores = make(map[string]bool, len(s.stores))
	for k := range s.stores {
		c.stores[k] = true
	}
	c.storeKeys = append([]string(nil), s.storeKeys...)
	return &c
}

func (g *codegen) store(s *symbols, name string) {
	g.enter()
	defer g.leave()
	s.addStore(name)
	if _, ok := s.refs[name]; ok {
		return
	}
	if s.parent != nil {
		if outer, ok := g.findRef(s.parent, name); ok {
			g.defineRef(s, name, &symLoad{loadAlias, outer})
			return
		}
	}
	g.defineRef(s, name, &symLoad{loadUndefined, ""})
}

func (g *codegen) declareParameter(s *symbols, name string) string {
	g.enter()
	defer g.leave()
	s.addStore(name)
	return g.defineRef(s, name, &symLoad{loadParameter, ""})
}

func (g *codegen) load(s *symbols, name string) {
	g.enter()
	defer g.leave()
	if _, ok := g.findRef(s, name); !ok {
		g.defineRef(s, name, &symLoad{loadResolve, name})
	}
}

// Symbols.branch_update, merging if branches and fixing the loads of names only some branches stored
func (g *codegen) branchUpdate(s *symbols, branches []*symbols) {
	g.enter()
	defer g.leave()
	var only []string
	seen := map[string]bool{}
	for _, b := range branches {
		for _, k := range b.storeKeys {
			if !seen[k] && !s.stores[k] {
				only = append(only, k)
			}
			seen[k] = true
		}
	}
	for _, b := range branches {
		for k, v := range b.refs {
			s.refs[k] = v
		}
		for _, k := range b.loadKeys {
			s.setLoad(k, b.loads[k])
		}
		for _, k := range b.storeKeys {
			s.addStore(k)
		}
	}
	for _, name := range only {
		target, _ := g.findRef(s, name)
		if s.parent != nil {
			if outer, ok := g.findRef(s.parent, name); ok {
				s.setLoad(target, symLoad{loadAlias, outer})
				continue
			}
		}
		s.setLoad(target, symLoad{loadResolve, name})
	}
}

// Symbols.dump_stores, each stored name mapped to its reference, nearest scope first and sorted per scope
func (g *codegen) dumpStores(s *symbols) ([]string, map[string]string) {
	g.enter()
	defer g.leave()
	var names []string
	refs := map[string]string{}
	for n := s; n != nil; n = n.parent {
		level := append([]string(nil), n.storeKeys...)
		sort.Strings(level)
		for _, name := range level {
			if _, ok := refs[name]; !ok {
				refs[name], _ = g.findRef(s, name)
				names = append(names, name)
			}
		}
	}
	return names, refs
}

// Symbols.dump_param_targets, which reads only this scope's loads on every level as jinja2 does
func (g *codegen) dumpParamTargets(s *symbols) map[string]bool {
	g.enter()
	defer g.leave()
	out := map[string]bool{}
	for _, k := range s.loadKeys {
		if s.loads[k].action == loadParameter {
			out[k] = true
		}
	}
	return out
}

// Symbols.analyze_node through RootVisitor, for_branch naming the for loop branch to analyze
func (g *codegen) analyzeNode(s *symbols, n *jnode, forBranch string) {
	g.enter()
	defer g.leave()
	g.need(2)
	v := &symVisitor{g: g, sym: s}
	g.enter()
	defer g.leave()
	g.need(1)
	g.enter()
	defer g.leave()
	switch n.cls {
	case "Template", "Block", "Macro", "FilterBlock", "Scope", "If", "ScopedEvalContextModifier":
		g.eachChild(n, nil, func(c *jnode) { v.visit(c, false) })
	case "AssignBlock":
		for _, c := range n.many("body") {
			v.visit(c, false)
		}
	case "CallBlock":
		g.eachChild(n, map[string]bool{"call": true}, func(c *jnode) { v.visit(c, false) })
	case "For":
		var branch []*jnode
		switch forBranch {
		case "body":
			v.visit(n.one("target"), true)
			branch = n.many("body")
		case "else":
			branch = n.many("else_")
		case "test":
			v.visit(n.one("target"), true)
			if t := n.one("test"); t != nil {
				v.visit(t, false)
			}
		}
		for _, c := range branch {
			v.visit(c, false)
		}
	case "With":
		for _, t := range n.many("targets") {
			v.visit(t, false)
		}
		for _, c := range n.many("body") {
			v.visit(c, false)
		}
	}
}

// Iterates direct children as Node.iter_child_nodes yields them, the generator frames pushed while producing each child
func (g *codegen) eachChild(n *jnode, exclude map[string]bool, f func(*jnode)) {
	for _, s := range n.slots {
		if exclude[s.name] {
			continue
		}
		if s.list {
			for _, c := range s.many {
				g.need(2)
				f(c)
			}
		} else if s.one != nil {
			g.need(2)
			f(s.one)
		}
	}
	g.need(2)
}

// jinja2's FrameSymbolVisitor over one scope's symbols
type symVisitor struct {
	g   *codegen
	sym *symbols
}

// NodeVisitor.visit dispatching to the FrameSymbolVisitor method for the node's class
func (v *symVisitor) visit(n *jnode, storeAsParam bool) {
	g := v.g
	g.enter()
	defer g.leave()
	g.need(1)
	g.enter()
	defer g.leave()
	switch n.cls {
	case "Name":
		switch {
		case storeAsParam || n.ctx == "param":
			g.declareParameter(v.sym, n.name)
		case n.ctx == "store":
			g.store(v.sym, n.name)
		case n.ctx == "load":
			g.load(v.sym, n.name)
		}
	case "NSRef":
		g.load(v.sym, n.name)
	case "If":
		v.visit(n.one("test"), storeAsParam)
		original := v.sym
		inner := func(nodes []*jnode) *symbols {
			g.enter()
			defer g.leave()
			rv := g.copySymbols(original)
			v.sym = rv
			for _, c := range nodes {
				v.visit(c, storeAsParam)
			}
			v.sym = original
			return rv
		}
		body := inner(n.many("body"))
		elifs := inner(n.many("elif_"))
		elses := inner(n.many("else_"))
		g.branchUpdate(v.sym, []*symbols{body, elifs, elses})
	case "Macro":
		g.store(v.sym, n.name)
	case "Import":
		v.generic(n, storeAsParam)
		g.store(v.sym, n.name)
	case "FromImport":
		v.generic(n, storeAsParam)
		for _, imp := range n.names {
			if imp.alias != "" {
				g.store(v.sym, imp.alias)
			} else {
				g.store(v.sym, imp.name)
			}
		}
	case "Assign":
		v.visit(n.one("node"), storeAsParam)
		v.visit(n.one("target"), storeAsParam)
	case "For":
		v.visit(n.one("iter"), storeAsParam)
	case "CallBlock":
		v.visit(n.one("call"), storeAsParam)
	case "FilterBlock":
		v.visit(n.one("filter"), storeAsParam)
	case "With":
		for _, t := range n.many("values") {
			v.visit(t, false)
		}
	case "AssignBlock":
		v.visit(n.one("target"), storeAsParam)
	case "Scope", "Block":
	default:
		g.eachChild(n, nil, func(c *jnode) { v.visit(c, storeAsParam) })
	}
}

// NodeVisitor.generic_visit, visiting each child with the same arguments
func (v *symVisitor) generic(n *jnode, storeAsParam bool) {
	v.g.enter()
	defer v.g.leave()
	v.g.eachChild(n, nil, func(c *jnode) { v.visit(c, storeAsParam) })
}

// find_undeclared, which of the names the nodes load before any store or parameter hides them, stopping at blocks
func (g *codegen) findUndeclared(nodes []*jnode, viaChildren bool, names ...string) map[string]bool {
	g.enter()
	defer g.leave()
	g.need(1)
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	found := map[string]bool{}
	done := false
	var visit func(n *jnode)
	visit = func(n *jnode) {
		g.enter()
		defer g.leave()
		g.need(1)
		g.enter()
		defer g.leave()
		switch n.cls {
		case "Name":
			if n.ctx == "load" && want[n.name] {
				found[n.name] = true
				done = sameSet(found, want)
			} else {
				delete(want, n.name)
			}
		case "Block":
		default:
			for _, c := range n.children() {
				g.need(2)
				if visit(c); done {
					return
				}
			}
			g.need(2)
		}
	}
	for _, n := range nodes {
		if viaChildren {
			g.need(2)
		}
		if visit(n); done {
			return found
		}
	}
	if viaChildren {
		g.need(2)
	}
	return found
}

func sameSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}
