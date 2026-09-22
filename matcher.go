package jsondiff

// This file unifies the four responsibilities that participate
// in array comparison, so that every equality candidate between
// a source and a target element is deep-compared at most once:
//
//   - equality (deepEqual), cached behind stable value tokens
//   - hashing (hasher.digest), used only as a pre-filter and
//     always verified by deep equality
//   - the longest common subsequence, computed over the cached
//     equality matrix instead of re-evaluating pairs
//   - move/copy candidate discovery, which reads the same matrix
//
// All diagnostic counters live on matchCtx, which is created at
// the beginning of a single Compare invocation. They never escape
// a comparison and are therefore isolated across concurrent calls.

// matchStats holds diagnostic counters for a single Compare
// invocation. The counters are purely diagnostic: they do not
// influence the generated patch in any way.
type matchStats struct {
	// equalCalls is the number of deepEqual comparisons
	// actually performed.
	equalCalls int
	// equalReuses is the number of times an already known
	// pair equality result was served from the cache. In the
	// legacy implementation every such reuse was a repeated
	// deepEqual call (LCS backtracking and move/copy scans).
	equalReuses int
	// digestCalls is the number of value digests computed.
	// Hash buckets are never trusted without an equality check.
	digestCalls int
	// hashCollisions counts the number of hash-bucket hits for
	// which every colliding value failed the equality check.
	hashCollisions int
	// candidates is the number of move/copy candidate pairs
	// resolved. Each pair is resolved at most once.
	candidates int
}

// matchCtx carries the per-comparison matching state.
// A fresh instance is created by Compare, which guarantees that
// concurrent comparisons on separate Differ values never share
// counters, tokens or caches.
type matchCtx struct {
	nextID uint32

	// vals holds every tokenized value for the lifetime of
	// the comparison. Values are never used as map keys, so
	// incomparable values such as slices and maps are safe.
	vals []interface{}

	// eq memoizes equality results between value tokens.
	// 0 = unknown, 1 = equal, 2 = unequal.
	eq map[[2]uint32]uint8

	// dig memoizes value digests. The boolean records whether
	// the unordered ("equivalent") digest variant was used.
	dig map[uint64]uint64

	hasher hasher
	stats  matchStats

	// injectFail, when non-nil, is invoked once per Compare from
	// a post-hash verification point. It exists solely to make
	// the recoverable-failure contract testable and is nil on
	// the production path.
	injectFail func()
	failDone   bool
}

func newMatchCtx() *matchCtx {
	return &matchCtx{
		eq:  make(map[[2]uint32]uint8),
		dig: make(map[uint64]uint64),
	}
}

// token registers v and returns its stable identity for the
// comparison. The same value never receives two different tokens
// within one comparison, even across nested arrays.
func (c *matchCtx) token(v interface{}) uint32 {
	id := c.nextID
	c.nextID++
	c.vals = append(c.vals, v)
	return id
}

// tokenKey packs a token and the sort flag into a digest-cache
// key. Sorting (unordered equivalence) is a distinct input.
func tokenKey(id uint32, sort bool) uint64 {
	k := uint64(id) << 1
	if sort {
		k |= 1
	}
	return k
}

// digest computes the hash of a tokenized value, caching the
// result. The hash is only ever used as a pre-filter; callers
// must verify candidate equality through matchCtx.equal.
func (c *matchCtx) digest(id uint32, sort bool) uint64 {
	k := tokenKey(id, sort)
	if h, ok := c.dig[k]; ok {
		return h
	}
	c.stats.digestCalls++
	h := c.hasher.digest(c.vals[id], sort)
	c.dig[k] = h
	return h
}

// equal returns whether the two tokenized values are deeply
// equal, performing at most one deepEqual for any ordered pair.
func (c *matchCtx) equal(a, b uint32) bool {
	if a == b {
		c.stats.equalReuses++
		return true
	}
	if a > b {
		a, b = b, a
	}
	key := [2]uint32{a, b}
	switch c.eq[key] {
	case 1:
		c.stats.equalReuses++
		return true
	case 2:
		c.stats.equalReuses++
		return false
	}
	c.stats.equalCalls++
	v := deepEqual(c.vals[a], c.vals[b])
	if v {
		c.eq[key] = 1
	} else {
		c.eq[key] = 2
	}
	return v
}

// maybeInjectFail triggers the test-only recoverable failure
// hook at most once per comparison.
func (c *matchCtx) maybeInjectFail() {
	if c.injectFail != nil && !c.failDone {
		c.failDone = true
		c.injectFail()
	}
}

// arrayMatcher owns the equality matrix of one pair of arrays.
type arrayMatcher struct {
	ctx      *matchCtx
	src, tgt []interface{}
	sids     []uint32
	tids     []uint32

	// m[i][j] reports whether src[i] equals tgt[j]. Cells are
	// computed lazily and never more than once; every cell is a
	// candidate at the intersection of equality, LCS and
	// move/copy discovery.
	m [][]uint8

	// legacyBacktrack counts matrix cells that the legacy LCS
	// implementation re-evaluated while backtracking. It is
	// diagnostic only; it quantifies the comparisons the unified
	// matcher avoids.
	legacyBacktrack int
}

const (
	matchUnknown uint8 = iota
	matchEqual
	matchUnequal
)

func newArrayMatcher(ctx *matchCtx, src, tgt []interface{}) *arrayMatcher {
	m := &arrayMatcher{
		ctx: ctx,
		src: src,
		tgt: tgt,
		m:   make([][]uint8, len(src)),
	}
	m.sids = make([]uint32, len(src))
	for i, v := range src {
		m.sids[i] = ctx.token(v)
	}
	m.tids = make([]uint32, len(tgt))
	for j, v := range tgt {
		m.tids[j] = ctx.token(v)
	}
	for i := range m.m {
		m.m[i] = make([]uint8, len(tgt))
	}
	return m
}

// equal reports whether src[i] equals tgt[j], evaluating the
// candidate pair at most once.
func (m *arrayMatcher) equal(i, j int) bool {
	switch m.m[i][j] {
	case matchEqual:
		m.ctx.stats.equalReuses++
		return true
	case matchUnequal:
		m.ctx.stats.equalReuses++
		return false
	}
	if m.ctx.equal(m.sids[i], m.tids[j]) {
		m.m[i][j] = matchEqual
		return true
	}
	m.m[i][j] = matchUnequal
	return false
}

// lcs returns the source/target index pairs of the longest
// common subsequence. It fills the equality matrix once and
// reuses it during backtracking, preserving exactly the
// tie-breaking rules of the previous implementation.
func (m *arrayMatcher) lcs() [][2]int {
	n, k := len(m.src), len(m.tgt)
	t := make([][]int, n+1)
	for i := 0; i <= n; i++ {
		t[i] = make([]int, k+1)
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= k; j++ {
			if m.equal(i-1, j-1) {
				t[i][j] = t[i-1][j-1] + 1
			} else {
				t[i][j] = max(t[i-1][j], t[i][j-1])
			}
		}
	}
	i, j := n, k
	s := make([][2]int, 0, t[i][j])

	for i > 0 && j > 0 {
		m.legacyBacktrack++
		switch {
		case m.equal(i-1, j-1):
			s = append(s, [2]int{i - 1, j - 1})
			i--
			j--
		case t[i-1][j] > t[i][j-1]:
			i--
		default:
			j--
		}
	}
	for x, y := 0, len(s)-1; x < y; x, y = x+1, y-1 {
		s[x], s[y] = s[y], s[x]
	}
	return s
}

// candidate resolves whether src element i can be moved onto
// target element j. It reads the already computed equality
// matrix and therefore performs no additional deep comparison.
func (m *arrayMatcher) candidate(i, j int) bool {
	m.ctx.stats.candidates++
	return m.equal(i, j)
}
