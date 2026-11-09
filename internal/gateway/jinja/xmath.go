package jinja

import (
	"math"
	"math/big"
	"sync"
)

// Working precision of the correctly rounded functions, far beyond float64's 53 bits
const xprec = 320

var (
	piOnce  sync.Once
	bigPi   *big.Float
	bigLn2  *big.Float
	bigPiP2 *big.Float
)

func bf(x float64) *big.Float { return new(big.Float).SetPrec(xprec).SetFloat64(x) }

func newBF() *big.Float { return new(big.Float).SetPrec(xprec) }

// Pi with enough bits to reduce any float64 argument, and ln 2, computed once
func constants() {
	piOnce.Do(func() {
		const piPrec = 1400 + xprec
		atanInv := func(n int64, prec uint) *big.Float {
			x := new(big.Float).SetPrec(prec).Quo(new(big.Float).SetPrec(prec).SetInt64(1), new(big.Float).SetPrec(prec).SetInt64(n))
			x2 := new(big.Float).SetPrec(prec).Mul(x, x)
			sum := new(big.Float).SetPrec(prec).Set(x)
			term := new(big.Float).SetPrec(prec).Set(x)
			eps := new(big.Float).SetPrec(prec).SetMantExp(big.NewFloat(1), -int(prec)-8)
			for k := int64(1); ; k++ {
				term.Mul(term, x2)
				t := new(big.Float).SetPrec(prec).Quo(term, new(big.Float).SetPrec(prec).SetInt64(2*k+1))
				if k%2 == 1 {
					sum.Sub(sum, t)
				} else {
					sum.Add(sum, t)
				}
				if t.Cmp(eps) < 0 {
					return sum
				}
			}
		}
		a := atanInv(5, piPrec)
		a.Mul(a, new(big.Float).SetPrec(piPrec).SetInt64(16))
		b := atanInv(239, piPrec)
		b.Mul(b, new(big.Float).SetPrec(piPrec).SetInt64(4))
		bigPi = a.Sub(a, b)
		bigPiP2 = new(big.Float).SetPrec(piPrec).Quo(bigPi, new(big.Float).SetPrec(piPrec).SetInt64(2))
		third := newBF().Quo(bf(1), bf(3))
		bigLn2 = atanhSeries(third)
		bigLn2.Mul(bigLn2, bf(2))
	})
}

// The series of atanh for small arguments
func atanhSeries(z *big.Float) *big.Float {
	z2 := newBF().Mul(z, z)
	sum := newBF().Set(z)
	term := newBF().Set(z)
	eps := new(big.Float).SetMantExp(big.NewFloat(1), -xprec-8)
	for k := int64(1); ; k++ {
		term.Mul(term, z2)
		t := newBF().Quo(term, bf(float64(2*k+1)))
		sum.Add(sum, t)
		if new(big.Float).Abs(t).Cmp(eps) < 0 {
			return sum
		}
	}
}

// Natural logarithm of a positive finite value
func bigLog(x *big.Float) *big.Float {
	constants()
	m := newBF()
	e := x.MantExp(m)
	z := newBF().Quo(newBF().Sub(m, bf(1)), newBF().Add(m, bf(1)))
	r := atanhSeries(z)
	r.Mul(r, bf(2))
	return r.Add(r, newBF().Mul(bigLn2, bf(float64(e))))
}

// Exponential of a value whose result fits a float64 exponent range with room to spare
func bigExp(x *big.Float) *big.Float {
	constants()
	kf := newBF().Quo(x, bigLn2)
	k, _ := kf.Int64()
	r := newBF().Sub(x, newBF().Mul(bigLn2, bf(float64(k))))
	sum := bf(1)
	term := bf(1)
	eps := new(big.Float).SetMantExp(big.NewFloat(1), -xprec-8)
	for n := int64(1); ; n++ {
		term.Mul(term, r)
		term.Quo(term, bf(float64(n)))
		sum.Add(sum, term)
		if new(big.Float).Abs(term).Cmp(eps) < 0 {
			break
		}
	}
	return sum.SetMantExp(sum, int(k))
}

// x raised to y for positive finite x and finite y, correctly rounded
func xPow(x, y float64) float64 {
	if y == 0 || x == 1 {
		return 1
	}
	l := bigLog(bf(x))
	l.Mul(l, bf(y))
	lf, _ := l.Float64()
	if lf > 710 {
		return math.Inf(1)
	}
	if lf < -746 {
		return 0
	}
	r, _ := bigExp(l).Float64()
	return r
}

// e raised to x, correctly rounded
func xExp(x float64) float64 {
	switch {
	case math.IsNaN(x):
		return x
	case x > 710:
		return math.Inf(1)
	case x < -746:
		return 0
	}
	r, _ := bigExp(bf(x)).Float64()
	return r
}

// Natural logarithm, correctly rounded, with C's special values
func xLog(x float64) float64 {
	if x <= 0 || math.IsInf(x, 0) || math.IsNaN(x) {
		return math.Log(x)
	}
	r, _ := bigLog(bf(x)).Float64()
	return r
}

// Reduces x modulo pi over 2, returning the remainder and quadrant
func reduceHalfPi(x float64) (*big.Float, int) {
	constants()
	prec := bigPiP2.Prec()
	bx := new(big.Float).SetPrec(prec).SetFloat64(x)
	q := new(big.Float).SetPrec(prec).Quo(bx, bigPiP2)
	n := new(big.Int)
	q.Int(n)
	if q.Sign() < 0 {
		n.Sub(n, big.NewInt(1))
	}
	r := new(big.Float).SetPrec(prec).Sub(bx, new(big.Float).SetPrec(prec).Mul(new(big.Float).SetPrec(prec).SetInt(n), bigPiP2))
	half := new(big.Float).SetPrec(prec).Quo(bigPiP2, new(big.Float).SetPrec(prec).SetInt64(2))
	if r.Cmp(half) > 0 {
		r.Sub(r, bigPiP2)
		n.Add(n, big.NewInt(1))
	}
	quad := int(new(big.Int).And(n, big.NewInt(3)).Int64())
	return newBF().Set(r), quad
}

// Taylor series of sin, or of cos when cosine is set, for a reduced argument
func trigSeries(r *big.Float, cosine bool) *big.Float {
	r2 := newBF().Mul(r, r)
	var term *big.Float
	start := int64(1)
	if cosine {
		term = bf(1)
		start = 2
	} else {
		term = newBF().Set(r)
		start = 3
	}
	sum := newBF().Set(term)
	eps := new(big.Float).SetMantExp(big.NewFloat(1), -xprec-8)
	for n := start; ; n += 2 {
		term.Mul(term, r2)
		term.Quo(term, bf(float64(n*(n-1))))
		term.Neg(term)
		sum.Add(sum, term)
		if new(big.Float).Abs(term).Cmp(eps) < 0 {
			return sum
		}
	}
}

// Sine and cosine, correctly rounded for finite arguments
func xSinCos(x float64) (float64, float64) {
	if math.IsInf(x, 0) || math.IsNaN(x) {
		return math.NaN(), math.NaN()
	}
	if x == 0 {
		return x, 1
	}
	r, quad := reduceHalfPi(x)
	s := trigSeries(r, false)
	c := trigSeries(r, true)
	switch quad {
	case 1:
		s, c = c, s.Neg(s)
	case 2:
		s, c = s.Neg(s), c.Neg(c)
	case 3:
		s, c = c.Neg(c), s
	}
	sf, _ := s.Float64()
	cf, _ := c.Float64()
	return sf, cf
}

// Arctangent of a big value, by halving the argument until the series converges fast
func bigAtan(x *big.Float) *big.Float {
	constants()
	if x.Sign() < 0 {
		r := bigAtan(newBF().Neg(x))
		return r.Neg(r)
	}
	if x.Cmp(bf(1)) > 0 {
		r := bigAtan(newBF().Quo(bf(1), x))
		pi2 := newBF().Set(bigPiP2)
		return pi2.Sub(pi2, r)
	}
	halvings := 0
	for x.Cmp(bf(0.125)) > 0 {
		s := newBF().Sqrt(newBF().Add(bf(1), newBF().Mul(x, x)))
		x = newBF().Quo(x, s.Add(s, bf(1)))
		halvings++
	}
	x2 := newBF().Mul(x, x)
	sum := newBF().Set(x)
	term := newBF().Set(x)
	eps := new(big.Float).SetMantExp(big.NewFloat(1), -xprec-8)
	for k := int64(1); ; k++ {
		term.Mul(term, x2)
		t := newBF().Quo(term, bf(float64(2*k+1)))
		if k%2 == 1 {
			sum.Sub(sum, t)
		} else {
			sum.Add(sum, t)
		}
		if t.Cmp(eps) < 0 {
			break
		}
	}
	return sum.SetMantExp(sum, halvings)
}

// atan2 with C's special values and correctly rounded finite results
func xAtan2(y, x float64) float64 {
	if y == 0 || x == 0 || math.IsInf(x, 0) || math.IsInf(y, 0) || math.IsNaN(x) || math.IsNaN(y) {
		return math.Atan2(y, x)
	}
	constants()
	a := bigAtan(newBF().Quo(bf(y), bf(x)))
	if x < 0 {
		pi := newBF().Set(bigPi)
		if y > 0 {
			a.Add(a, pi)
		} else {
			a.Sub(a, pi)
		}
	}
	r, _ := a.Float64()
	return r
}

// The exactly rounded hypotenuse with C's special values
func xHypot(x, y float64) float64 {
	if math.IsInf(x, 0) || math.IsInf(y, 0) || math.IsNaN(x) || math.IsNaN(y) {
		return math.Hypot(x, y)
	}
	s := newBF().Add(newBF().Mul(bf(x), bf(x)), newBF().Mul(bf(y), bf(y)))
	r, _ := s.Sqrt(s).Float64()
	return r
}
