package jsondiff

// matchStats collects diagnostic counters describing the
// work performed while matching arrays and discovering
// move/copy candidates.
//
// The counters are purely diagnostic: they never influence
// the generated patch. They live on the Differ for the
// duration of a single comparison and are reset before
// every comparison, so concurrent comparisons are isolated
// and a failed comparison never leaks its counters into a
// successful one.
type matchStats struct {
	// comparisons counts the old/new equality candidates
	// that were actually evaluated with a deep equality
	// predicate. Each distinct pair of values is evaluated
	// at most once per comparison.
	comparisons int
	// digests counts the distinct value digests computed;
	// each distinct value is hashed at most once.
	digests int
	// candidateReuse counts the times a candidate answer
	// was served from cache instead of being re-evaluated.
	candidateReuse int
	// lcsTables counts the LCS tables built (one per array
	// compared with the LCS option).
	lcsTables int
}

// arrayMatcher centralizes the four responsibilities that
// participate in matching the elements of two arrays:
//
//  1. equality  — deciding whether an old and a new element
//     are deeply equal (delegated to the comparison-wide
//     eqOracle),
//  2. hashing   — digesting every element exactly once and
//     using digests only as routing/negative hints,
//  3. matching  — deriving the longest common subsequence
//     (LCS) pairs from the equality matrix,
//  4. discovery — exposing the equality matrix so that
//     move/copy candidates reuse the same evaluations
//     instead of being recomputed.
//
// Both the positional and LCS-based array comparison
// strategies use the same matcher; no parallel
// implementation is maintained for tests or for options.
type arrayMatcher struct {
	src []interface{}
	tgt []interface{}
	d   *Differ
	o         *eqOracle
	unordered bool

	// Per-position digests, computed lazily, once.
	srcHash []uint64
	tgtHash []uint64
	srcSet  []bool
	tgtSet  []bool

	// eq[i][j] caches the equality verdict for a position
	// pair: eqUnknown / eqEqual / eqDiffer.
	eq [][]uint8

	// Lazily-built LCS table and pairs.
	table [][]int
	pairs [][2]int
}

const (
	eqUnknown uint8 = 0
	eqEqual   uint8 = 1
	eqDiffer  uint8 = 2
)

// newArrayMatcher returns a matcher bound to the
// comparison-wide equality oracle. When unordered is true,
// digests are computed over order-insensitive values, which
// mirrors the historical behavior of the Equivalent option.
func newArrayMatcher(d *Differ, o *eqOracle, src, tgt []interface{}, unordered bool) *arrayMatcher {
	return &arrayMatcher{
		src:       src,
		tgt:       tgt,
		d:         d,
		o:         o,
		unordered: unordered,
	}
}

func (m *arrayMatcher) digest(v interface{}) uint64 {
	return m.o.digestOf(v, m.unordered)
}

func (m *arrayMatcher) srcDigest(i int) uint64 {
	if m.srcHash == nil {
		m.srcHash = make([]uint64, len(m.src))
		m.srcSet = make([]bool, len(m.src))
	}
	if !m.srcSet[i] {
		m.srcHash[i] = m.digest(m.src[i])
		m.srcSet[i] = true
	}
	return m.srcHash[i]
}

func (m *arrayMatcher) tgtDigest(j int) uint64 {
	if m.tgtHash == nil {
		m.tgtHash = make([]uint64, len(m.tgt))
		m.tgtSet = make([]bool, len(m.tgt))
	}
	if !m.tgtSet[j] {
		m.tgtHash[j] = m.digest(m.tgt[j])
		m.tgtSet[j] = true
	}
	return m.tgtHash[j]
}

// equal reports whether src[i] and tgt[j] are deeply equal.
// Each position pair is evaluated at most once. The verdict
// comes from the comparison oracle, which confirms digest
// bucket hits with deep equality: a hash collision can
// never pair two distinct elements, and repeated elements
// are matched correctly.
func (m *arrayMatcher) equal(i, j int) bool {
	if m.eq == nil {
		m.eq = make([][]uint8, len(m.src))
		for k := range m.eq {
			m.eq[k] = make([]uint8, len(m.tgt))
		}
	}
	if v := m.eq[i][j]; v != eqUnknown {
		m.d.diag.candidateReuse++
		return v == eqEqual
	}
	if m.o.equal(m.src[i], m.tgt[j], m.srcDigest(i), m.tgtDigest(j)) {
		m.eq[i][j] = eqEqual
		return true
	}
	m.eq[i][j] = eqDiffer
	return false
}

// lcsTable builds (once) the classic LCS length table using
// the matcher equality predicate.
func (m *arrayMatcher) lcsTable() [][]int {
	if m.table != nil {
		return m.table
	}
	n, p := len(m.src), len(m.tgt)
	t := make([][]int, n+1)
	for i := 0; i <= n; i++ {
		t[i] = make([]int, p+1)
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= p; j++ {
			if m.equal(i-1, j-1) {
				t[i][j] = t[i-1][j-1] + 1
			} else {
				t[i][j] = max(t[i-1][j], t[i][j-1])
			}
		}
	}
	m.table = t
	m.d.diag.lcsTables++
	return t
}

// lcsPairs returns (once) the index pairs that constitute
// the longest common subsequence. The traceback reuses the
// equality matrix populated while building the table, so no
// candidate is ever evaluated twice.
func (m *arrayMatcher) lcsPairs() [][2]int {
	if m.pairs != nil {
		return m.pairs
	}
	t := m.lcsTable()
	i, j := len(m.src), len(m.tgt)
	s := make([][2]int, 0, t[i][j])

	for i > 0 && j > 0 {
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
	for a, b := 0, len(s)-1; a < b; a, b = a+1, b-1 {
		s[a], s[b] = s[b], s[a]
	}
	m.pairs = s
	return s
}

// unorderedEqual reports whether both arrays hold the same
// multiset of elements regardless of their order. Elements
// are partitioned by digest and each candidate pair inside
// a bucket is confirmed through the collision-safe oracle,
// with every pair evaluated at most once.
func (m *arrayMatcher) unorderedEqual() bool {
	if len(m.src) != len(m.tgt) {
		return false
	}
	used := make([]bool, len(m.tgt))
	for i := range m.src {
		matched := false
		for j := range m.tgt {
			if used[j] {
				continue
			}
			// Digests are a cheap negative filter; unequal
			// digests prove distinct values, equal digests
			// still require the oracle confirmation.
			if m.srcDigest(i) != m.tgtDigest(j) {
				continue
			}
			if m.equal(i, j) {
				used[j] = true
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}
