package jinja

import "strings"

// Whether vLLM hands this template message content as lists of text parts, by vLLM's own detection
func detectOpenAIFormat(body []stmt) bool {
	var sets []*setStmt
	var fors []*forStmt
	var macros []*macroStmt
	var calls []*callExpr
	walkStmts(body, func(n any) bool {
		switch x := n.(type) {
		case *setStmt:
			sets = append(sets, x)
		case *forStmt:
			fors = append(fors, x)
		case *macroStmt:
			macros = append(macros, x)
		case *callExpr:
			calls = append(calls, x)
		}
		return true
	})
	messagesVars := aliasesOf(sets, "messages")
	var messageVars []string
	for _, loop := range fors {
		for _, v := range messagesVars {
			if isVarOrElemsAccess(loop.iter, v, "") {
				target, ok := loop.target.(*nameExpr)
				if !ok {
					return false
				}
				messageVars = append(messageVars, target.name)
				break
			}
		}
	}
	accessesContent := func(e expr) bool {
		for _, v := range messageVars {
			if isVarOrElemsAccess(e, v, "content") {
				return true
			}
		}
		return false
	}
	paramsByLoop := map[*forStmt]map[string]bool{}
	inMacro := map[*forStmt]bool{}
	for _, m := range macros {
		params := map[string]bool{}
		for _, p := range m.sig.params {
			params[p] = true
		}
		contentParams := map[string]bool{}
		for _, c := range calls {
			fn, ok := c.fn.(*nameExpr)
			if !ok || fn.name != m.name {
				continue
			}
			for i, a := range c.args.args {
				if i < len(m.sig.params) && accessesContent(a) {
					contentParams[m.sig.params[i]] = true
				}
			}
			for i, k := range c.args.kwNames {
				if params[k] && accessesContent(c.args.kwVals[i]) {
					contentParams[k] = true
				}
			}
		}
		walkStmts(m.sig.body, func(n any) bool {
			if loop, ok := n.(*forStmt); ok {
				inMacro[loop] = true
				if len(contentParams) > 0 {
					paramsByLoop[loop] = contentParams
				}
			}
			return true
		})
	}
	for _, loop := range fors {
		matched := false
		for _, v := range messageVars {
			if isVarOrElemsAccess(loop.iter, v, "content") {
				matched = true
				break
			}
		}
		iter, iterIsName := loop.iter.(*nameExpr)
		if !matched && iterIsName {
			matched = paramsByLoop[loop][iter.name] || !inMacro[loop] && iter.name == "content"
		}
		if matched {
			_, ok := loop.target.(*nameExpr)
			return ok
		}
	}
	return false
}

// The names a variable reaches through assignments and namespace attributes, breadth first
func aliasesOf(sets []*setStmt, varname string) []string {
	names := []string{varname}
	seen := map[string]bool{varname: true}
	queue := []string{varname}
	enqueue := func(name, related string) {
		names = append(names, name)
		if name != related && !seen[name] {
			seen[name] = true
			queue = append(queue, name)
		}
	}
	for len(queue) > 0 {
		related := queue[0]
		queue = queue[1:]
		for _, s := range sets {
			lhs, isName := s.target.(*nameExpr)
			if isName && isVarOrElemsAccess(s.value, related, "") {
				enqueue(lhs.name, related)
			}
			var aliases []string
			if call, ok := s.value.(*callExpr); isName && ok {
				if fn, ok := call.fn.(*nameExpr); ok && fn.name == "namespace" {
					for i, k := range call.args.kwNames {
						if isVarOrElemsAccess(call.args.kwVals[i], related, "") {
							aliases = append(aliases, lhs.name+"."+k)
						}
					}
				}
			} else if ns, ok := s.target.(*nsRefExpr); ok && isVarOrElemsAccess(s.value, related, "") {
				aliases = append(aliases, ns.name+"."+ns.attr)
			}
			for _, a := range aliases {
				enqueue(a, related)
			}
		}
	}
	return names
}

func isVarAccess(e expr, varname string) bool {
	switch x := e.(type) {
	case *nameExpr:
		return x.name == varname
	case *getattrExpr:
		if base, attr, ok := cutLast(varname, "."); ok {
			return x.attr == attr && isVarAccess(x.obj, base)
		}
	}
	return false
}

func cutLast(s, sep string) (string, string, bool) {
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return "", "", false
	}
	return s[:i], s[i+len(sep):], true
}

func isAttrAccess(e expr, varname, key string) bool {
	switch x := e.(type) {
	case *getitemExpr:
		c, ok := x.arg.(*constExpr)
		return ok && isVarAccess(x.obj, varname) && pyEq(c.v, key)
	case *getattrExpr:
		return isVarAccess(x.obj, varname) && x.attr == key
	}
	return false
}

// vLLM's test for an expression reading a variable or its key, through filters, tests and slices
func isVarOrElemsAccess(e expr, varname, key string) bool {
	switch x := e.(type) {
	case *filterExpr:
		if _, buffered := x.node.(*bufferExpr); buffered {
			return false
		}
		return isVarOrElemsAccess(x.node, varname, key)
	case *testExpr:
		return isVarOrElemsAccess(x.node, varname, key)
	case *getitemExpr:
		if _, ok := x.arg.(*sliceExpr); ok {
			return isVarOrElemsAccess(x.obj, varname, key)
		}
	}
	if key != "" {
		return isAttrAccess(e, varname, key)
	}
	return isVarAccess(e, varname)
}
