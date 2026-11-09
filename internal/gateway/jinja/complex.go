package jinja

import (
	"math"
	"strconv"
)

// A complex operand as CPython's TO_COMPLEX reads it
func toComplex(v Value) (complex128, error) {
	if c, ok := v.(complex128); ok {
		return c, nil
	}
	f, err := toFloat(v)
	return complex(f, 0), err
}

// Complex arithmetic as CPython 3.14 does it, real operands kept real in mixed operations
func complexOp(op string, a, b Value) (Value, error) {
	if op == "pow" {
		x, err := toComplex(a)
		if err != nil {
			return nil, err
		}
		y, err := toComplex(b)
		if err != nil {
			return nil, err
		}
		return complexPow(x, y)
	}
	if op == "floordiv" || op == "mod" {
		return nil, unsupported(op, a, b)
	}
	ca, aComplex := a.(complex128)
	cb, bComplex := b.(complex128)
	var ra, rb float64
	var err error
	if !aComplex {
		if ra, err = toFloat(a); err != nil {
			return nil, err
		}
	}
	if !bComplex {
		if rb, err = toFloat(b); err != nil {
			return nil, err
		}
	}
	switch op {
	case "add":
		switch {
		case aComplex && bComplex:
			return complex(real(ca)+real(cb), imag(ca)+imag(cb)), nil
		case aComplex:
			return complex(real(ca)+rb, imag(ca)), nil
		}
		return complex(real(cb)+ra, imag(cb)), nil
	case "sub":
		switch {
		case aComplex && bComplex:
			return complex(real(ca)-real(cb), imag(ca)-imag(cb)), nil
		case aComplex:
			return complex(real(ca)-rb, imag(ca)), nil
		}
		return complex(ra-real(cb), -imag(cb)), nil
	case "mul":
		switch {
		case aComplex && bComplex:
			return complexProd(ca, cb), nil
		case aComplex:
			return complex(real(ca)*rb, imag(ca)*rb), nil
		}
		return complex(real(cb)*ra, imag(cb)*ra), nil
	case "div":
		var q complex128
		var ok bool
		switch {
		case aComplex && bComplex:
			q, ok = complexQuot(ca, cb)
		case aComplex:
			if rb == 0 {
				return nil, errorf("ZeroDivisionError", "division by zero")
			}
			q, ok = complex(real(ca)/rb, imag(ca)/rb), true
		default:
			q, ok = realComplexQuot(ra, cb)
		}
		if !ok {
			return nil, errorf("ZeroDivisionError", "division by zero")
		}
		return q, nil
	}
	return nil, unsupported(op, a, b)
}

func copysign1(cond bool, sign float64) float64 {
	if cond {
		return math.Copysign(1, sign)
	}
	return math.Copysign(0, sign)
}

// CPython's _Py_c_prod with its recovery of infinities
func complexProd(z, w complex128) complex128 {
	a, b, c, d := real(z), imag(z), real(w), imag(w)
	ac, bd, ad, bc := float64(a*c), float64(b*d), float64(a*d), float64(b*c)
	re, im := ac-bd, ad+bc
	if !math.IsNaN(re) || !math.IsNaN(im) {
		return complex(re, im)
	}
	zeroNaN := func(x *float64) {
		if math.IsNaN(*x) {
			*x = math.Copysign(0, *x)
		}
	}
	recalc := false
	if math.IsInf(a, 0) || math.IsInf(b, 0) {
		a, b = copysign1(math.IsInf(a, 0), a), copysign1(math.IsInf(b, 0), b)
		zeroNaN(&c)
		zeroNaN(&d)
		recalc = true
	}
	if math.IsInf(c, 0) || math.IsInf(d, 0) {
		c, d = copysign1(math.IsInf(c, 0), c), copysign1(math.IsInf(d, 0), d)
		zeroNaN(&a)
		zeroNaN(&b)
		recalc = true
	}
	if !recalc && (math.IsInf(ac, 0) || math.IsInf(bd, 0) || math.IsInf(ad, 0) || math.IsInf(bc, 0)) {
		zeroNaN(&a)
		zeroNaN(&b)
		zeroNaN(&c)
		zeroNaN(&d)
		recalc = true
	}
	if recalc {
		inf := math.Inf(1)
		re = inf * (float64(a*c) - float64(b*d))
		im = inf * (float64(a*d) + float64(b*c))
	}
	return complex(re, im)
}

// CPython's _Py_c_quot, Smith's division with recovery of infinities and zeros, false when dividing by zero
func complexQuot(a, b complex128) (complex128, bool) {
	ar, ai, br, bi := real(a), imag(a), real(b), imag(b)
	absR, absI := math.Abs(br), math.Abs(bi)
	var re, im float64
	switch {
	case absR >= absI:
		if absR == 0 {
			return 0, false
		}
		ratio := bi / br
		denom := br + float64(bi*ratio)
		re = (ar + float64(ai*ratio)) / denom
		im = (ai - float64(ar*ratio)) / denom
	case absI >= absR:
		ratio := br / bi
		denom := float64(br*ratio) + bi
		re = (float64(ar*ratio) + ai) / denom
		im = (float64(ai*ratio) - ar) / denom
	default:
		re, im = math.NaN(), math.NaN()
	}
	finite := func(x float64) bool { return !math.IsInf(x, 0) && !math.IsNaN(x) }
	if math.IsNaN(re) && math.IsNaN(im) {
		inf := math.Inf(1)
		if (math.IsInf(ar, 0) || math.IsInf(ai, 0)) && finite(br) && finite(bi) {
			x, y := copysign1(math.IsInf(ar, 0), ar), copysign1(math.IsInf(ai, 0), ai)
			re = inf * (float64(x*br) + float64(y*bi))
			im = inf * (float64(y*br) - float64(x*bi))
		} else if (math.IsInf(absR, 0) || math.IsInf(absI, 0)) && finite(ar) && finite(ai) {
			x, y := copysign1(math.IsInf(br, 0), br), copysign1(math.IsInf(bi, 0), bi)
			re = 0 * (float64(ar*x) + float64(ai*y))
			im = 0 * (float64(ai*x) - float64(ar*y))
		}
	}
	return complex(re, im), true
}

// CPython's _Py_rc_quot dividing a real by a complex
func realComplexQuot(a float64, b complex128) (complex128, bool) {
	br, bi := real(b), imag(b)
	absR, absI := math.Abs(br), math.Abs(bi)
	var re, im float64
	switch {
	case absR >= absI:
		if absR == 0 {
			return 0, false
		}
		ratio := bi / br
		denom := br + float64(bi*ratio)
		re = a / denom
		im = float64(-a*ratio) / denom
	case absI >= absR:
		ratio := br / bi
		denom := float64(br*ratio) + bi
		re = float64(a*ratio) / denom
		im = -a / denom
	default:
		re, im = math.NaN(), math.NaN()
	}
	if math.IsNaN(re) && math.IsNaN(im) && !math.IsInf(a, 0) && !math.IsNaN(a) && (math.IsInf(absR, 0) || math.IsInf(absI, 0)) {
		x, y := copysign1(math.IsInf(br, 0), br), copysign1(math.IsInf(bi, 0), bi)
		re = 0 * float64(a*x)
		im = 0 * float64(-a*y)
	}
	return complex(re, im), true
}

// CPython's complex_pow, repeated squaring for small integer exponents and polar form otherwise
func complexPow(a, b complex128) (Value, error) {
	var p complex128
	edom := false
	if imag(b) == 0 && real(b) == math.Floor(real(b)) && math.Abs(real(b)) <= 100 {
		n := int64(real(b))
		powu := func(x complex128, n int64) complex128 {
			r, pw := complex128(1), x
			for mask := int64(1); mask > 0 && n >= mask; mask <<= 1 {
				if n&mask != 0 {
					r = complexProd(r, pw)
				}
				pw = complexProd(pw, pw)
			}
			return r
		}
		if n > 0 {
			p = powu(a, n)
		} else {
			var ok bool
			p, ok = complexQuot(1, powu(a, -n))
			edom = !ok
		}
	} else {
		switch {
		case b == 0:
			p = 1
		case a == 0:
			edom = imag(b) != 0 || real(b) < 0
			p = 0
		default:
			vabs := xHypot(real(a), imag(a))
			length := math.Pow(vabs, real(b))
			if vabs > 0 && !math.IsInf(vabs, 0) && !math.IsNaN(real(b)) && !math.IsInf(real(b), 0) {
				length = xPow(vabs, real(b))
			}
			at := xAtan2(imag(a), real(a))
			phase := float64(at * real(b))
			if imag(b) != 0 {
				length *= xExp(float64(-at * imag(b)))
				phase += float64(imag(b) * xLog(vabs))
			}
			sin, cos := xSinCos(phase)
			p = complex(float64(length*cos), float64(length*sin))
		}
	}
	if edom {
		return nil, errorf("ZeroDivisionError", "zero to a negative or complex power")
	}
	if math.IsInf(real(p), 0) || math.IsInf(imag(p), 0) {
		return nil, errorf("OverflowError", "complex exponentiation")
	}
	return p, nil
}

// CPython's complex_abs
func complexAbs(c complex128) (Value, error) {
	re, im := real(c), imag(c)
	switch {
	case math.IsInf(re, 0):
		return math.Abs(re), nil
	case math.IsInf(im, 0):
		return math.Abs(im), nil
	case math.IsNaN(re) || math.IsNaN(im):
		return math.NaN(), nil
	}
	r := xHypot(re, im)
	if math.IsInf(r, 0) {
		return nil, errorf("OverflowError", "absolute value too large")
	}
	return r, nil
}

// CPython's complex repr, the imaginary part alone when the real part is positive zero
func complexRepr(c complex128) string {
	re, im := real(c), imag(c)
	if re == 0 && !math.Signbit(re) {
		return pyFloatFormat(im, 'r', 0, false, false, false, false, false) + "j"
	}
	return "(" + pyFloatFormat(re, 'r', 0, false, false, false, false, false) + pyFloatFormat(im, 'r', 0, false, false, true, false, false) + "j)"
}

// Python == between a complex and another value
func complexEq(c complex128, other Value) bool {
	switch o := other.(type) {
	case complex128:
		return real(c) == real(o) && imag(c) == imag(o)
	case float64:
		return real(c) == o && imag(c) == 0
	case bool, int64:
		if imag(c) != 0 {
			return false
		}
		cmp, ok := numCompare(real(c), o)
		return ok && cmp == 0
	}
	if isIntegral(other) {
		if imag(c) != 0 {
			return false
		}
		cmp, ok := numCompare(real(c), other)
		return ok && cmp == 0
	}
	return false
}

// CPython's format of a complex with a format specification
func formatComplex(c complex128, spec string) (string, error) {
	if spec == "" {
		return complexRepr(c), nil
	}
	fs, err := parseFormatSpec(spec)
	if err == nil {
		err = checkPrecision(fs.prec)
	}
	if err != nil {
		return "", err
	}
	if fs.zero || fs.hasFill && fs.fill == '0' {
		return "", errorf("ValueError", "Zero padding is not allowed in complex format specifier")
	}
	if fs.align == '=' {
		return "", errorf("ValueError", "'=' alignment flag is not allowed in complex format specifier")
	}
	re, im := real(c), imag(c)
	typ := fs.typ
	prec := fs.prec
	skipRe, parens := false, false
	switch typ {
	case 0:
		if re == 0 && !math.Signbit(re) {
			skipRe = true
		} else {
			parens = true
		}
		typ = 'r'
		if prec >= 0 {
			typ = 'g'
		} else {
			prec = 0
		}
	case 'n':
		typ = 'g'
		if prec < 0 {
			prec = 6
		}
	case 'e', 'E', 'f', 'F', 'g', 'G':
		if prec < 0 {
			prec = 6
		}
	default:
		return "", errorf("ValueError", "Unknown format code '%c' for object of type 'complex'", typ)
	}
	addDot0 := fs.typ == 0 && fs.prec >= 0
	reStr := pyFloatFormat(re, typ, prec, fs.alt, addDot0, fs.sign == '+', fs.sign == ' ', fs.noNegZero)
	imStr := pyFloatFormat(im, typ, prec, fs.alt, addDot0, true, false, fs.noNegZero)
	body := imStr + "j"
	if !skipRe {
		body = reStr + body
	}
	if parens {
		body = "(" + body + ")"
	}
	align := fs.align
	if align == 0 {
		align = '>'
	}
	return alignText(body, fs.fill, align, fs.width), nil
}

func complexHashKey(c complex128) (string, bool) {
	if imag(c) == 0 {
		return "", false
	}
	return "c" + strconv.FormatUint(math.Float64bits(real(c)), 16) + ":" + strconv.FormatUint(math.Float64bits(imag(c)), 16), true
}

func complexAttr(c complex128, name string) (Value, bool, error) {
	switch name {
	case "real":
		return real(c), true, nil
	case "imag":
		return imag(c), true, nil
	case "conjugate":
		return method(c, name, nil, func([]Value) (Value, error) { return complex(real(c), -imag(c)), nil }), true, nil
	}
	return nil, false, nil
}

func isComplex(v Value) bool {
	_, ok := v.(complex128)
	return ok
}
