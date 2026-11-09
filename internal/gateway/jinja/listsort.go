package jinja

// CPython's listsort from Objects/listobject.c, ported so sorts compare keys in exactly the order and pairs Python does

const (
	listsortMaxMinrun = 64
	listsortMinGallop = 7
)

// One sort run in progress, its start, length and powersort power
type listsortRun struct {
	base, len, power int
}

// The state of one listsort call over key indices
type listsortState struct {
	a         []int
	lt        func(x, y int) (bool, error)
	minGallop int
	pending   []listsortRun
}

// The order list.sort leaves n keys in, comparing with lt as listsort does until a comparison fails
func listsort(n int, reverse bool, lt func(x, y int) (bool, error)) ([]int, error) {
	a := make([]int, n)
	for i := range a {
		a[i] = i
	}
	if n < 2 {
		return a, nil
	}
	if reverse {
		reverseInts(a)
	}
	ms := &listsortState{a: a, lt: lt, minGallop: listsortMinGallop}
	minrun := mergeComputeMinrun(n)
	lo, remaining := 0, n
	for remaining > 0 {
		run, err := ms.countRun(lo, remaining)
		if err != nil {
			return nil, err
		}
		if run < minrun {
			force := min(remaining, minrun)
			if err := ms.binarysort(lo, force, run); err != nil {
				return nil, err
			}
			run = force
		}
		if err := ms.foundNewRun(run, n); err != nil {
			return nil, err
		}
		ms.pending = append(ms.pending, listsortRun{base: lo, len: run})
		lo += run
		remaining -= run
	}
	if err := ms.mergeForceCollapse(); err != nil {
		return nil, err
	}
	if reverse {
		reverseInts(a)
	}
	return a, nil
}

func reverseInts(a []int) {
	for i, j := 0, len(a)-1; i < j; i, j = i+1, j-1 {
		a[i], a[j] = a[j], a[i]
	}
}

func mergeComputeMinrun(n int) int {
	r := 0
	for n >= listsortMaxMinrun {
		r |= n & 1
		n >>= 1
	}
	return n + r
}

// Sorts n keys from lo by binary insertion, the first ok of them already sorted
func (ms *listsortState) binarysort(lo, n, ok int) error {
	a := ms.a[lo : lo+n]
	if ok == 0 {
		ok = 1
	}
	for ; ok < n; ok++ {
		l, r := 0, ok
		pivot := a[ok]
		for l < r {
			m := (l + r) >> 1
			less, err := ms.lt(pivot, a[m])
			if err != nil {
				return err
			}
			if less {
				r = m
			} else {
				l = m + 1
			}
		}
		copy(a[l+1:ok+1], a[l:ok])
		a[l] = pivot
	}
	return nil
}

// The length of the run starting at lo, at most remaining long, reversing a descending run in place
func (ms *listsortState) countRun(lo, remaining int) (int, error) {
	a := ms.a[lo : lo+remaining]
	nextSmaller := func(n int) (bool, error) { return ms.lt(a[n], a[n-1]) }
	n := 1
	for ; n < remaining; n++ {
		smaller, err := nextSmaller(n)
		if err != nil {
			return 0, err
		}
		if smaller {
			break
		}
	}
	if n == remaining {
		return n, nil
	}
	if n > 1 {
		less, err := ms.lt(a[0], a[n-1])
		if err != nil {
			return 0, err
		}
		if less {
			return n, nil
		}
		reverseInts(a[:n])
	}
	n++
	neq := 0
	reverseLastNeq := func() {
		if neq > 0 {
			neq++
			reverseInts(a[n-neq : n])
			neq = 0
		}
	}
	for ; n < remaining; n++ {
		smaller, err := nextSmaller(n)
		if err != nil {
			return 0, err
		}
		if smaller {
			reverseLastNeq()
			continue
		}
		larger, err := ms.lt(a[n-1], a[n])
		if err != nil {
			return 0, err
		}
		if larger {
			break
		}
		neq++
	}
	reverseLastNeq()
	reverseInts(a[:n])
	for ; n < remaining; n++ {
		smaller, err := nextSmaller(n)
		if err != nil {
			return 0, err
		}
		if smaller {
			break
		}
	}
	return n, nil
}

// The powersort power of the boundary after the run at s1 of length n1, the next run n2 long
func powerloop(s1, n1, n2, n int) int {
	result := 0
	a := 2*s1 + n1
	b := a + n1 + n2
	for {
		result++
		if a >= n {
			a -= n
			b -= n
		} else if b >= n {
			break
		}
		a <<= 1
		b <<= 1
	}
	return result
}

// Merges pending runs whose power exceeds the new run's boundary power, as found_new_run does
func (ms *listsortState) foundNewRun(n2, n int) error {
	if len(ms.pending) == 0 {
		return nil
	}
	p := ms.pending
	last := p[len(p)-1]
	power := powerloop(last.base, last.len, n2, n)
	for len(ms.pending) > 1 && ms.pending[len(ms.pending)-2].power > power {
		if err := ms.mergeAt(len(ms.pending) - 2); err != nil {
			return err
		}
	}
	ms.pending[len(ms.pending)-1].power = power
	return nil
}

func (ms *listsortState) mergeForceCollapse() error {
	for len(ms.pending) > 1 {
		n := len(ms.pending) - 2
		if n > 0 && ms.pending[n-1].len < ms.pending[n+1].len {
			n--
		}
		if err := ms.mergeAt(n); err != nil {
			return err
		}
	}
	return nil
}

// Merges pending runs i and i+1
func (ms *listsortState) mergeAt(i int) error {
	ra, rb := ms.pending[i], ms.pending[i+1]
	ms.pending[i].len = ra.len + rb.len
	ms.pending = append(ms.pending[:i+1], ms.pending[i+2:]...)
	k, err := ms.gallopRight(ms.a[rb.base], ms.a[ra.base:ra.base+ra.len], 0)
	if err != nil {
		return err
	}
	baseA, na := ra.base+k, ra.len-k
	if na == 0 {
		return nil
	}
	nb, err := ms.gallopLeft(ms.a[baseA+na-1], ms.a[rb.base:rb.base+rb.len], rb.len-1)
	if err != nil || nb <= 0 {
		return err
	}
	if na <= nb {
		return ms.mergeLo(baseA, na, rb.base, nb)
	}
	return ms.mergeHi(baseA, na, rb.base, nb)
}

// The leftmost position in sorted a where key belongs, galloping from hint
func (ms *listsortState) gallopLeft(key int, a []int, hint int) (int, error) {
	n := len(a)
	lastofs, ofs := 0, 1
	less, err := ms.lt(a[hint], key)
	if err != nil {
		return 0, err
	}
	if less {
		maxofs := n - hint
		for ofs < maxofs {
			l, err := ms.lt(a[hint+ofs], key)
			if err != nil {
				return 0, err
			}
			if !l {
				break
			}
			lastofs = ofs
			ofs = ofs<<1 + 1
		}
		ofs = min(ofs, maxofs)
		lastofs += hint
		ofs += hint
	} else {
		maxofs := hint + 1
		for ofs < maxofs {
			l, err := ms.lt(a[hint-ofs], key)
			if err != nil {
				return 0, err
			}
			if l {
				break
			}
			lastofs = ofs
			ofs = ofs<<1 + 1
		}
		ofs = min(ofs, maxofs)
		lastofs, ofs = hint-ofs, hint-lastofs
	}
	lastofs++
	for lastofs < ofs {
		m := lastofs + (ofs-lastofs)>>1
		l, err := ms.lt(a[m], key)
		if err != nil {
			return 0, err
		}
		if l {
			lastofs = m + 1
		} else {
			ofs = m
		}
	}
	return ofs, nil
}

// The position right of every key equal to key in sorted a, galloping from hint
func (ms *listsortState) gallopRight(key int, a []int, hint int) (int, error) {
	n := len(a)
	lastofs, ofs := 0, 1
	less, err := ms.lt(key, a[hint])
	if err != nil {
		return 0, err
	}
	if less {
		maxofs := hint + 1
		for ofs < maxofs {
			l, err := ms.lt(key, a[hint-ofs])
			if err != nil {
				return 0, err
			}
			if !l {
				break
			}
			lastofs = ofs
			ofs = ofs<<1 + 1
		}
		ofs = min(ofs, maxofs)
		lastofs, ofs = hint-ofs, hint-lastofs
	} else {
		maxofs := n - hint
		for ofs < maxofs {
			l, err := ms.lt(key, a[hint+ofs])
			if err != nil {
				return 0, err
			}
			if l {
				break
			}
			lastofs = ofs
			ofs = ofs<<1 + 1
		}
		ofs = min(ofs, maxofs)
		lastofs += hint
		ofs += hint
	}
	lastofs++
	for lastofs < ofs {
		m := lastofs + (ofs-lastofs)>>1
		l, err := ms.lt(key, a[m])
		if err != nil {
			return 0, err
		}
		if l {
			ofs = m
		} else {
			lastofs = m + 1
		}
	}
	return ofs, nil
}

// Merges the runs at pa and pb in place, copying the shorter first run out as merge_lo does
func (ms *listsortState) mergeLo(pa, na, pb, nb int) error {
	tmp := append([]int(nil), ms.a[pa:pa+na]...)
	a, d, ia, ib := ms.a, pa, 0, pb
	finish := func() {
		copy(a[d:d+na], tmp[ia:ia+na])
	}
	copyB := func() {
		copy(a[d:d+nb], a[ib:ib+nb])
		a[d+nb] = tmp[ia]
	}
	a[d] = a[ib]
	d, ib, nb = d+1, ib+1, nb-1
	if nb == 0 {
		finish()
		return nil
	}
	if na == 1 {
		copyB()
		return nil
	}
	minGallop := ms.minGallop
	for {
		acount, bcount := 0, 0
		for {
			less, err := ms.lt(a[ib], tmp[ia])
			if err != nil {
				finish()
				return err
			}
			if less {
				a[d] = a[ib]
				d, ib, nb = d+1, ib+1, nb-1
				bcount++
				acount = 0
				if nb == 0 {
					finish()
					return nil
				}
				if bcount >= minGallop {
					break
				}
			} else {
				a[d] = tmp[ia]
				d, ia, na = d+1, ia+1, na-1
				acount++
				bcount = 0
				if na == 1 {
					copyB()
					return nil
				}
				if acount >= minGallop {
					break
				}
			}
		}
		minGallop++
		for {
			if minGallop > 1 {
				minGallop--
			}
			ms.minGallop = minGallop
			k, err := ms.gallopRight(a[ib], tmp[ia:ia+na], 0)
			if err != nil {
				finish()
				return err
			}
			acount = k
			if k > 0 {
				copy(a[d:d+k], tmp[ia:ia+k])
				d, ia, na = d+k, ia+k, na-k
				if na == 1 {
					copyB()
					return nil
				}
				if na == 0 {
					return nil
				}
			}
			a[d] = a[ib]
			d, ib, nb = d+1, ib+1, nb-1
			if nb == 0 {
				finish()
				return nil
			}
			k, err = ms.gallopLeft(tmp[ia], a[ib:ib+nb], 0)
			if err != nil {
				finish()
				return err
			}
			bcount = k
			if k > 0 {
				copy(a[d:d+k], a[ib:ib+k])
				d, ib, nb = d+k, ib+k, nb-k
				if nb == 0 {
					finish()
					return nil
				}
			}
			a[d] = tmp[ia]
			d, ia, na = d+1, ia+1, na-1
			if na == 1 {
				copyB()
				return nil
			}
			if acount < listsortMinGallop && bcount < listsortMinGallop {
				break
			}
		}
		minGallop++
		ms.minGallop = minGallop
	}
}

// Merges the runs at pa and pb from the right, copying the shorter second run out as merge_hi does
func (ms *listsortState) mergeHi(pa, na, pb, nb int) error {
	tmp := append([]int(nil), ms.a[pb:pb+nb]...)
	a := ms.a
	d := pb + nb - 1
	ia := pa + na - 1
	ib := nb - 1
	finish := func() {
		copy(a[d-(nb-1):d+1], tmp[:nb])
	}
	copyA := func() {
		d -= na
		ia -= na
		copy(a[d+1:d+1+na], a[ia+1:ia+1+na])
		a[d] = tmp[ib]
	}
	a[d] = a[ia]
	d, ia, na = d-1, ia-1, na-1
	if na == 0 {
		finish()
		return nil
	}
	if nb == 1 {
		copyA()
		return nil
	}
	minGallop := ms.minGallop
	for {
		acount, bcount := 0, 0
		for {
			less, err := ms.lt(tmp[ib], a[ia])
			if err != nil {
				finish()
				return err
			}
			if less {
				a[d] = a[ia]
				d, ia, na = d-1, ia-1, na-1
				acount++
				bcount = 0
				if na == 0 {
					finish()
					return nil
				}
				if acount >= minGallop {
					break
				}
			} else {
				a[d] = tmp[ib]
				d, ib, nb = d-1, ib-1, nb-1
				bcount++
				acount = 0
				if nb == 1 {
					copyA()
					return nil
				}
				if bcount >= minGallop {
					break
				}
			}
		}
		minGallop++
		for {
			if minGallop > 1 {
				minGallop--
			}
			ms.minGallop = minGallop
			k, err := ms.gallopRight(tmp[ib], a[pa:pa+na], na-1)
			if err != nil {
				finish()
				return err
			}
			k = na - k
			acount = k
			if k > 0 {
				d -= k
				ia -= k
				copy(a[d+1:d+1+k], a[ia+1:ia+1+k])
				na -= k
				if na == 0 {
					finish()
					return nil
				}
			}
			a[d] = tmp[ib]
			d, ib, nb = d-1, ib-1, nb-1
			if nb == 1 {
				copyA()
				return nil
			}
			k, err = ms.gallopLeft(a[ia], tmp[:nb], nb-1)
			if err != nil {
				finish()
				return err
			}
			k = nb - k
			bcount = k
			if k > 0 {
				d -= k
				ib -= k
				copy(a[d+1:d+1+k], tmp[ib+1:ib+1+k])
				nb -= k
				if nb == 1 {
					copyA()
					return nil
				}
				if nb == 0 {
					return nil
				}
			}
			a[d] = a[ia]
			d, ia, na = d-1, ia-1, na-1
			if na == 0 {
				finish()
				return nil
			}
			if acount < listsortMinGallop && bcount < listsortMinGallop {
				break
			}
		}
		minGallop++
		ms.minGallop = minGallop
	}
}
