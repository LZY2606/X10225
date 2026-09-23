package jsondiff

// matchDiagnostics holds counters that describe the amount
// of work performed while matching arrays and discovering
// move/copy candidates during a single comparison.
//
// The counters are diagnostic-only: they never influence the
// generated patch, and they are reset at the beginning of
// every comparison. A Differ is not safe for concurrent use,
// so each concurrent comparison must use its own Differ; the
// counters are then isolated as well.
type matchDiagnostics struct {
	// equalCalls counts the number of old/new element
	// comparisons evaluated while filling and backtracking
	// the LCS tables. Repeated comparisons of the same
	// index pair are served from the memo and are not
	// counted again.
	equalCalls int
	// candidateChecks counts the number of deep equality
	// checks performed while resolving a move or copy
	// candidate against a hash bucket.
	candidateChecks int
	// hashCollisions counts the number of times distinct
	// values were found inside the same hash bucket.
	hashCollisions int
}

// arrayMatcher unifies the four responsibilities involved
// in matching two arrays:
//   - equality: the memoized old/new element predicate
//   - hashing:   only used as a pre-filter by callers
//   - LCS:       the longest common subsequence table
//   - candidates: move/copy resolution by callers
//
// Each old/new element pair is compared at most once, and
// the diagnostic counters quantify the number of comparisons
// that were required.
type arrayMatcher struct {
	src     []interface{}
	tgt     []interface{}
	diag    *matchDiagnostics
	memo    map[uint64]uint8
	hook    func(matcherHookStage)
}

const (
	matchUnknown uint8 = iota
	matchEqual
	matchNotEqual
)

type matcherHookStage uint8

const (
	hookBeforeLCSFill matcherHookStage = iota
	hookBeforeLCSBacktrack
	hookAfterLCS
)

// equalAt reports whether src[i] and tgt[j] are deeply
// equal. The result is memoized so that every old/new
// index pair is compared at most once.
func (m *arrayMatcher) equalAt(i, j int) bool {
	key := uint64(i)*uint64(len(m.tgt)+1) + uint64(j)

	if r, ok := m.memo[key]; ok {
		return r == matchEqual
	}
	if m.diag != nil {
		m.diag.equalCalls++
	}
	var r uint8
	if deepEqual(m.src[i], m.tgt[j]) {
		r = matchEqual
	} else {
		r = matchNotEqual
	}
	if m.memo == nil {
		m.memo = make(map[uint64]uint8)
	}
	m.memo[key] = r
	return r == matchEqual
}

// lcs computes the longest common subsequence and returns
// the ordered list of matching index pairs.
func (m *arrayMatcher) lcs() [][2]int {
	if m.hook != nil {
		m.hook(hookBeforeLCSFill)
	}
	t := make([][]int, len(m.src)+1)

	for i := 0; i <= len(m.src); i++ {
		t[i] = make([]int, len(m.tgt)+1)
	}
	for i := 1; i < len(t); i++ {
		for j := 1; j < len(t[i]); j++ {
			if m.equalAt(i-1, j-1) {
				t[i][j] = t[i-1][j-1] + 1
			} else {
				t[i][j] = max(t[i-1][j], t[i][j-1])
			}
		}
	}
	if m.hook != nil {
		m.hook(hookBeforeLCSBacktrack)
	}
	i, j := len(m.src), len(m.tgt)
	s := make([][2]int, 0, t[i][j])

	for i > 0 && j > 0 {
		switch {
		case m.equalAt(i-1, j-1):
			s = append(s, [2]int{i - 1, j - 1})
			i--
			j--
		case t[i-1][j] > t[i][j-1]:
			i--
		default:
			j--
		}
	}
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
	if m.hook != nil {
		m.hook(hookAfterLCS)
	}
	return s
}

// diagnostics returns the diagnostics collected during the
// most recent comparison. It is an unexported entry point
// reserved for in-package tests: it exposes counters only,
// and the counters are produced by the same production
// matching/candidate code paths, not a test-only copy.
func (d *Differ) diagnostics() matchDiagnostics {
	return d.diag
}

// removedCandidate is a "remove" operation that may later
// be promoted into a "move" operation when an equal value
// is added. The patchIndex tracks the current position of
// the operation in the live patch while later insertions
// and truncations reshape it.
type removedCandidate struct {
	patchIndex int
	op         Operation
	consumed   bool
}

// candidateIndex indexes the remove operations of the
// current patch by value hash. Hash buckets are checked
// with deep equality before a candidate is accepted, so
// hash collisions can never pair distinct values.
type candidateIndex struct {
	buckets map[uint64][]*removedCandidate
}

func (ci *candidateIndex) reset() {
	clear(ci.buckets)
}

func (ci *candidateIndex) add(index int, op Operation, h uint64) {
	if ci.buckets == nil {
		ci.buckets = make(map[uint64][]*removedCandidate)
	}
	ci.buckets[h] = append(ci.buckets[h], &removedCandidate{
		patchIndex: index,
		op:         op,
	})
}

// find returns the first non-consumed candidate whose value
// is deeply equal to v. Candidate lookup only computes the
// hash of v once; equality is verified for every entry of
// the matching bucket, and collisions are recorded.
func (ci *candidateIndex) find(v interface{}, h uint64, diag *matchDiagnostics, equal func(a, b interface{}) bool) (*removedCandidate, bool) {
	for _, c := range ci.buckets[h] {
		if c.consumed {
			continue
		}
		if diag != nil {
			diag.candidateChecks++
			if !equal(c.op.OldValue, v) {
				diag.hashCollisions++
				continue
			}
		} else if !equal(c.op.OldValue, v) {
			continue
		}
		return c, true
	}
	return nil, false
}

// insert shifts the indices of every candidate located at
// or after the insertion point.
func (ci *candidateIndex) insert(pos int) {
	for _, b := range ci.buckets {
		for _, c := range b {
			if c.patchIndex >= pos {
				c.patchIndex++
			}
		}
	}
}

// truncate drops every candidate located at or after pos,
// mirroring a patch truncation.
func (ci *candidateIndex) truncate(pos int) {
	for h, b := range ci.buckets {
		kept := b[:0]
		for _, c := range b {
			if c.patchIndex < pos {
				kept = append(kept, c)
			}
		}
		if len(kept) == 0 {
			delete(ci.buckets, h)
		} else {
			ci.buckets[h] = kept
		}
	}
}
