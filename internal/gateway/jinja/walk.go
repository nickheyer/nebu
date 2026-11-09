package jinja

import "math/big"

// Visits every statement and expression in document order, skipping children when visit returns false
func walkStmts(body []stmt, visit func(any) bool) {
	for _, s := range body {
		walkNode(s, visit)
	}
}

func walkNode(n any, visit func(any) bool) {
	if n == nil || !visit(n) {
		return
	}
	each := func(nodes ...any) {
		for _, c := range nodes {
			walkNode(c, visit)
		}
	}
	exprs := func(es []expr) {
		for _, e := range es {
			walkNode(e, visit)
		}
	}
	args := func(a callArgs) {
		exprs(a.args)
		exprs(a.kwVals)
		each(a.dynArgs, a.dynKwargs)
	}
	sig := func(s *signature) {
		exprs(s.defaults)
		walkStmts(s.body, visit)
	}
	switch x := n.(type) {
	case *outputStmt:
		each(x.expr)
	case *forStmt:
		each(x.target, x.iter)
		walkStmts(x.body, visit)
		walkStmts(x.orelse, visit)
		each(x.test)
	case *ifStmt:
		each(x.test)
		walkStmts(x.body, visit)
		for _, e := range x.elifs {
			walkNode(e, visit)
		}
		walkStmts(x.orelse, visit)
	case *setStmt:
		each(x.target, x.value)
	case *setBlockStmt:
		each(x.target)
		walkStmts(x.body, visit)
		each(x.filter)
	case *macroStmt:
		sig(x.sig)
	case *callBlockStmt:
		each(x.call)
		sig(x.sig)
	case *filterBlockStmt:
		walkStmts(x.body, visit)
		each(x.filter)
	case *withStmt:
		exprs(x.targets)
		exprs(x.values)
		walkStmts(x.body, visit)
	case *blockStmt:
		walkStmts(x.body, visit)
	case *autoescapeStmt:
		each(x.value)
		walkStmts(x.body, visit)
	case *loaderStmt:
		each(x.template)
	case *generationStmt:
		walkStmts(x.body, visit)
	case *tupleExpr:
		exprs(x.items)
	case *listExpr:
		exprs(x.items)
	case *dictExpr:
		for i := range x.keys {
			each(x.keys[i], x.vals[i])
		}
	case *getattrExpr:
		each(x.obj)
	case *getitemExpr:
		each(x.obj, x.arg)
	case *sliceExpr:
		each(x.start, x.stop, x.step)
	case *callExpr:
		each(x.fn)
		args(x.args)
	case *filterExpr:
		each(x.node)
		args(x.args)
	case *testExpr:
		each(x.node)
		args(x.args)
	case *condExpr:
		each(x.test, x.expr1, x.expr2)
	case *binExpr:
		each(x.left, x.right)
	case *andExpr:
		each(x.left, x.right)
	case *orExpr:
		each(x.left, x.right)
	case *notExpr:
		each(x.e)
	case *negExpr:
		each(x.e)
	case *posExpr:
		each(x.e)
	case *concatExpr:
		exprs(x.items)
	case *compareExpr:
		each(x.first)
		exprs(x.operands)
	}
}

// The ValueError visit_Const raises writing a constant whose repr holds an integer past the digit limit
func checkDigits(v Value) error {
	switch x := v.(type) {
	case *big.Int:
		_, err := decimalString(x)
		return err
	case *Range:
		for _, n := range []*big.Int{x.start, x.stop, x.step} {
			if _, err := decimalString(n); err != nil {
				return err
			}
		}
	case *List:
		return firstDigitsError(x.items)
	case *Tuple:
		return firstDigitsError(x.items)
	case *Dict:
		for i := range x.keys {
			if err := checkDigits(x.keys[i]); err != nil {
				return err
			}
			if err := checkDigits(x.vals[i]); err != nil {
				return err
			}
		}
	}
	return nil
}

func firstDigitsError(items []Value) error {
	for _, it := range items {
		if err := checkDigits(it); err != nil {
			return err
		}
	}
	return nil
}
