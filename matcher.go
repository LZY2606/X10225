package jsondiff

import (
	"encoding/json"
	"unsafe"
)

// errComparisonInterrupted is the unexported sentinel raised
// by injected test failures. It models a recoverable,
// mid-computation failure: public entry points translate it
// into a regular error, and no partial result is returned.
var errComparisonInterrupted = comparisonInterruptedError{}

type comparisonInterruptedError struct{}

func (comparisonInterruptedError) Error() string {
	return "jsondiff: comparison interrupted before completion"
}

// matchCounters holds diagnostic counters about the work
// performed by a single comparison. Counters are reset for
// every comparison and are purely diagnostic: they never
// influence the generated patch.
type matchCounters struct {
	digests        int // value digests computed
	digestCacheHit int // value digests served from cache
	equalCalls     int // candidate pairs for which equality was evaluated
	identityHits   int // equality resolved by value identity
	memoHits       int // equality served from the pair memo
	hashRejected   int // candidate pairs rejected solely by hash mismatch
	moveCandidates int // move (remove->add) candidates scanned
	copyCandidates int // copy (unchanged value) candidates scanned
}

// matchHooks groups test-only injection points used to
// exercise the matcher through the public entry points.
// The hooks are never set by production options.
type matchHooks struct {
	// digest overrides the digest of every value when set.
	digest func(val interface{}) (uint64, bool)
	// failAfter aborts the comparison, in a recoverable
	// manner, once failAfter equality evaluations have
	// been performed. Zero means never abort.
	failAfter int
	// yield is invoked once per LCS dynamic programming
	// cell, allowing tests to perturb scheduling without
	// affecting inputs or options.
	yield func(ai, bi int)
}

// matcher centralizes everything an array comparison needs:
// value identity, equality (deepEqual) memoization, hashing,
// LCS discovery and move/copy candidate discovery.
//
// A matcher is created for every comparison and is not
// shared between concurrent comparisons, so that diagnostic
// counters and caches are invocation-isolated.
type matcher struct {
	h          hasher
	scalars    map[scalarKey]int
	refs       map[valueRef]int
	nextID     int
	memo       map[pairKey]bool
	digestMemo map[int]uint64
	stats      matchCounters
	hooks      matchHooks
	evaluated  int // equality evaluations performed since creation
}

type scalarKey struct {
	typ byte
	// v holds the scalar value, represented so that JSON
	// types cannot collide with each other.
	v any
}

type valueRef struct {
	ptr unsafePointer
	len int
}

type pairKey struct {
	a, b int
}

type unsafePointer = unsafe.Pointer

func pointerOf(v interface{}) unsafePointer {
	switch val := v.(type) {
	case []interface{}:
		return unsafe.Pointer(unsafe.SliceData(val))
	case map[string]interface{}:
		return *(*unsafePointer)(unsafe.Pointer(&val))
	default:
		return nil
	}
}

func newMatcher(hooks matchHooks) *matcher {
	return &matcher{
		scalars:    make(map[scalarKey]int),
		refs:       make(map[valueRef]int),
		memo:       make(map[pairKey]bool),
		digestMemo: make(map[int]uint64),
		hooks:      hooks,
	}
}

func pairOf(a, b int) pairKey {
	if a > b {
		a, b = b, a
	}
	return pairKey{a, b}
}

// identify interns the value and returns its stable identity
// for the duration of the comparison. Scalars are identified
// by type+value; reference types use their storage location
// and length, so that distinct elements of an array are never
// conflated even when they hold equal content.
func (m *matcher) identify(v interface{}) int {
	switch val := v.(type) {
	case nil:
		return m.internScalar(scalarKey{typ: byte(jsonNull), v: nil})
	case string:
		return m.internScalar(scalarKey{typ: byte(jsonString), v: val})
	case bool:
		return m.internScalar(scalarKey{typ: byte(jsonBoolean), v: val})
	case float64:
		return m.internScalar(scalarKey{typ: byte(jsonNumberFloat), v: val})
	case json.Number:
		return m.internScalar(scalarKey{typ: byte(jsonNumberString), v: val})
	case []interface{}:
		return m.internRef(valueRef{ptr: pointerOf(val), len: len(val)})
	case map[string]interface{}:
		return m.internRef(valueRef{ptr: pointerOf(val), len: len(val)})
	default:
		panic(invalidJSONTypeError{t: v})
	}
}

func (m *matcher) internScalar(k scalarKey) int {
	if id, ok := m.scalars[k]; ok {
		return id
	}
	m.nextID++
	m.scalars[k] = m.nextID
	return m.nextID
}

func (m *matcher) internRef(r valueRef) int {
	if id, ok := m.refs[r]; ok {
		return id
	}
	m.nextID++
	m.refs[r] = m.nextID
	return m.nextID
}

// equal reports whether two values are JSON-equal. Each
// unordered candidate pair is evaluated at most once: the
// result is keyed on the interned identities, hashes are
// used only as a reject pre-filter, and a hash collision can
// never produce a false positive since deepEqual remains the
// source of truth.
func (m *matcher) equal(src, tgt interface{}) bool {
	idA, idB := m.identify(src), m.identify(tgt)
	if idA == idB {
		m.stats.identityHits++
		return true
	}
	key := pairOf(idA, idB)
	if eq, ok := m.memo[key]; ok {
		m.stats.memoHits++
		return eq
	}

	if m.hooks.failAfter > 0 && m.evaluated >= m.hooks.failAfter {
		panic(errComparisonInterrupted)
	}

	m.stats.equalCalls++
	m.evaluated++

	// Hash pre-filter: for comparable values, a hash
	// mismatch proves inequality without invoking deepEqual.
	// Equal hashes are never trusted blindly; deepEqual
	// remains the source of truth, so collisions cannot
	// produce false pairings.
	eq := false
	if jsonTypeSwitch(src) == jsonTypeSwitch(tgt) {
		ha := m.digestOf(src, false)
		hb := m.digestOf(tgt, false)
		if ha != hb {
			m.stats.hashRejected++
			m.memo[key] = false
			return false
		}
		eq = deepEqual(src, tgt)
	} else {
		eq = deepEqual(src, tgt)
	}
	m.memo[key] = eq
	return eq
}

// digestOf returns the hash of the value. Digests are
// memoized by value identity so that repeated matching of
// the same element is computed once.
func (m *matcher) digestOf(v interface{}, sort bool) uint64 {
	if m.hooks.digest != nil {
		if h, ok := m.hooks.digest(v); ok {
			return h
		}
	}
	if !sort {
		id := m.identify(v)
		if h, ok := m.digestMemo[id]; ok {
			m.stats.digestCacheHit++
			return h
		}
		m.stats.digests++
		h := m.h.digest(v, false)
		m.digestMemo[id] = h
		return h
	}
	// Sorting digests mutate arrays in place; any digest
	// cached for their identity must be discarded because
	// the layout has changed.
	m.stats.digests++
	h := m.h.digest(v, true)
	for k := range m.digestMemo {
		delete(m.digestMemo, k)
	}
	return h
}

// lcs computes the longest common subsequence of two slices
// using the matcher's memoized equality, and returns the
// matched index pairs in ascending order.
func (m *matcher) lcs(src, tgt []interface{}) [][2]int {
	n, k := len(src), len(tgt)
	tbl := make([][]int, n+1)
	for i := 0; i <= n; i++ {
		tbl[i] = make([]int, k+1)
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= k; j++ {
			if m.hooks.yield != nil {
				m.hooks.yield(i-1, j-1)
			}
			if m.equal(src[i-1], tgt[j-1]) {
				tbl[i][j] = tbl[i-1][j-1] + 1
			} else {
				tbl[i][j] = max(tbl[i-1][j], tbl[i][j-1])
			}
		}
	}
	i, j := n, k
	pairs := make([][2]int, 0, tbl[i][j])
	for i > 0 && j > 0 {
		switch {
		case m.equal(src[i-1], tgt[j-1]):
			pairs = append(pairs, [2]int{i - 1, j - 1})
			i--
			j--
		case tbl[i-1][j] > tbl[i][j-1]:
			i--
		default:
			j--
		}
	}
	for x, y := 0, len(pairs)-1; x < y; x, y = x+1, y-1 {
		pairs[x], pairs[y] = pairs[y], pairs[x]
	}
	return pairs
}

// unorderedEqual reports whether the two arrays contain the
// same multiset of values, regardless of order. Hashes only
// bucket candidates; multiplicity and equality are always
// confirmed with the memoized deep equality, so repeated
// values and hash collisions cannot cause a false positive.
func (m *matcher) unorderedEqual(src, tgt []interface{}, sort bool) bool {
	if len(src) != len(tgt) {
		return false
	}
	remaining := make([]int, len(tgt))
	for i := range remaining {
		remaining[i] = i
	}
	for _, sv := range src {
		chosen := -1
		for idx, ti := range remaining {
			if m.hashBucket(sv, tgt[ti], sort) && m.equal(sv, tgt[ti]) {
				chosen = idx
				break
			}
		}
		if chosen == -1 {
			return false
		}
		remaining = append(remaining[:chosen], remaining[chosen+1:]...)
	}
	return len(remaining) == 0
}

func (m *matcher) hashBucket(src, tgt interface{}, sort bool) bool {
	return m.digestOf(src, sort) == m.digestOf(tgt, sort)
}

// counters returns a copy of the diagnostic counters.
func (m *matcher) counters() matchCounters {
	return m.stats
}
