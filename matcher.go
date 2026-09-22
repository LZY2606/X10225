package jsondiff

// A pairMatcher computes the equality relation between the
// elements of a source and a target slice. It is the single
// point of truth shared by the LCS computation, so that each
// candidate pair is evaluated at most once: the elements are
// hashed exactly once, pairs with distinct hashes are known
// to be unequal without further comparison, and the result
// of the first deep comparison of a pair is cached for the
// remainder of the computation.
type pairMatcher struct {
	d       *Differ
	src     []interface{}
	tgt     []interface{}
	srcHash []uint64
	tgtHash []uint64
	cols    int
	known   []uint64 // bitset of evaluated pairs
	eq      []uint64 // bitset of equal pairs
}

func newPairMatcher(d *Differ, src, tgt []interface{}) *pairMatcher {
	m := &pairMatcher{
		d:       d,
		src:     src,
		tgt:     tgt,
		srcHash: make([]uint64, len(src)),
		tgtHash: make([]uint64, len(tgt)),
		cols:    len(tgt),
	}
	if n := len(src) * len(tgt); n > 0 {
		m.known = make([]uint64, (n+63)/64)
		m.eq = make([]uint64, (n+63)/64)
	}
	for i, v := range src {
		m.srcHash[i] = d.digest(v, false)
	}
	for j, v := range tgt {
		m.tgtHash[j] = d.digest(v, false)
	}
	return m
}

// equal reports whether src[i] and tgt[j] are deeply equal.
// Each pair is evaluated at most once; the result of the
// first evaluation is cached and returned on subsequent
// calls. Pairs with distinct hashes are known to be unequal,
// and pairs with colliding hashes are confirmed with a deep
// comparison, so that hash collisions never result in a
// mismatch of unequal values.
func (m *pairMatcher) equal(i, j int) bool {
	bit := i*m.cols + j
	w, b := uint(bit/64), uint(bit%64)
	if m.known[w]&(1<<b) != 0 {
		return m.eq[w]&(1<<b) != 0
	}
	eq := false
	if m.srcHash[i] == m.tgtHash[j] {
		eq = m.d.deepEqual(m.src[i], m.tgt[j])
	}
	m.known[w] |= 1 << b
	if eq {
		m.eq[w] |= 1 << b
	}
	return eq
}
