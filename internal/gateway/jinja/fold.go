package jinja

import (
	"fmt"
	"math"
)

// An expression jinja2 compiles to a name Python cannot resolve, inf or nan written as code
type nameErrorExpr struct{ name string }

// The values the code generator folds with, its renderer carrying the eval context's autoescape and the folder its volatility
type folder struct {
	r        *renderer
	volatile bool
}

// The first unresolvable name a constant's Python source would mention, inf or nan
func nonFiniteName(v Value) (string, bool) {
	floatName := func(f float64) (string, bool) {
		switch {
		case math.IsNaN(f):
			return "nan", true
		case math.IsInf(f, 0):
			return "inf", true
		}
		return "", false
	}
	switch x := v.(type) {
	case float64:
		return floatName(x)
	case complex128:
		if !(real(x) == 0 && !math.Signbit(real(x))) {
			if name, bad := floatName(real(x)); bad {
				return name, true
			}
		}
		if name, bad := floatName(imag(x)); bad {
			return name + "j", true
		}
	case *Tuple:
		return firstNonFinite(x.items)
	case *List:
		return firstNonFinite(x.items)
	case *Dict:
		for i := range x.keys {
			if name, bad := nonFiniteName(x.keys[i]); bad {
				return name, true
			}
			if name, bad := nonFiniteName(x.vals[i]); bad {
				return name, true
			}
		}
	}
	return "", false
}

func firstNonFinite(items []Value) (string, bool) {
	for _, it := range items {
		if name, bad := nonFiniteName(it); bad {
			return name, true
		}
	}
	return "", false
}

// A container rebuilt with fresh lists and dicts, as evaluating its Python literal again gives
func freshCopy(v Value) Value {
	switch x := v.(type) {
	case *List:
		items := make([]Value, len(x.items))
		for i, it := range x.items {
			items[i] = freshCopy(it)
		}
		return &List{items: items}
	case *Tuple:
		items := make([]Value, len(x.items))
		for i, it := range x.items {
			items[i] = freshCopy(it)
		}
		return &Tuple{items: items}
	case *Dict:
		d := newDict()
		for i, k := range x.keys {
			_ = d.set(k, freshCopy(x.vals[i]))
		}
		return d
	}
	return v
}

func (e *nameErrorExpr) err() error {
	return &Error{Kind: "NameError", Msg: fmt.Sprintf("name %s is not defined", strRepr(e.name))}
}
