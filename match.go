package jsondiff

import (
	"errors"
	"fmt"
)

// matchStage identifies the phase of array matching that
// triggered an old/new equality comparison. Stage values
// are part of the diagnostic surface and must remain
// stable; they are never exposed in a patch.
type matchStage string

const (
	stageLCS        matchStage = "lcs"
	stageMove       matchStage = "move-candidate"
	stageCopy       matchStage = "copy-candidate"
	stageEquivalent matchStage = "equivalent"
)

// errMatcherAborted is the recoverable, internal sentinel
// used when array matching is aborted before completion.
// It belongs to the same error layering as the other
// internal failures: it is raised as a panic on the
// Differ path and converted to a returned error by the
// public entry points.
var errMatcherAborted = errors.New("jsondiff: array matching aborted")

// matcherAbortError wraps errMatcherAborted with the
// diagnostic context of a recoverable failure.
type matcherAbortError struct {
	stage       matchStage
	comparisons int
	reason      string
}

func (e *matcherAbortError) Error() string {
	return fmt.Sprintf("%s: stage %q after %d comparison(s): %s",
		errMatcherAborted, e.stage, e.comparisons, e.reason)
}

func (e *matcherAbortError) Unwrap() error {
	return errMatcherAborted
}

// hashSite identifies the value whose digest is being
// computed, by its location in the compared document and
// whether the digest uses order-insensitive hashing.
type hashSite struct {
	path   string
	sorted bool
}

// hashOverride describes a synthetic digest for a value
// at a specific site. It is used exclusively by tests to
// reproduce hash collisions without forging any matcher
// result; the production hashing path stays shared.
type hashOverride struct {
	site hashSite
	hash uint64
}

// matchHook is the package-internal test seam of the
// matcher. Every hook is attached to a single Differ and
// therefore to a single comparison state; it cannot leak
// between calls or goroutines. It never replaces the
// matching algorithm, it only observes comparisons and may
// override digests or abort the run once.
type matchHook struct {
	// abortAt aborts the comparison in which the running
	// count of old/new equality checks reaches this value.
	// A value <= 0 disables abort injection.
	abortAt int
	reason  string

	// overrides forces the digest(s) of values located at
	// the listed sites. Other sites are hashed normally.
	overrides []hashOverride
}

func (h *matchHook) abortError(stage matchStage) error {
	reason := h.reason
	if reason == "" {
		reason = "injected recoverable failure"
	}
	return &matcherAbortError{
		stage:       stage,
		comparisons: h.abortAt,
		reason:      reason,
	}
}

func (h *matchHook) forcedDigest(site hashSite) (uint64, bool) {
	for i := range h.overrides {
		if h.overrides[i].site == site {
			return h.overrides[i].hash, true
		}
	}
	return 0, false
}

// eqResult is a tri-state equality memo entry. Unknown
// slots are represented by the zero value so that an
// aborted comparison never silently behaves as "equal".
type eqResult uint8

const (
	eqUnknown eqResult = iota
	eqTrue
	eqFalse
)

// eqLCSKey identifies a pair of positions compared while
// filling the LCS table of one array.
type eqLCSKey struct {
	scope  string
	srcIdx int
	tgtIdx int
}

// eqPathKey identifies a pair of document locations
// compared while evaluating a move/copy candidate or an
// order-insensitive equivalence check.
type eqPathKey struct {
	scope string
	a     string
	b     string
}

// hashKey memoizes the digest of a value at a site.
type hashKey struct {
	path   string
	sorted bool
}

// matchState holds the per-comparison scratch state of
// array matching: equality memos, digest memos and the
// diagnostic comparison counter. A fresh state is created
// for every public comparison, which guarantees that
// counters and caches are isolated between concurrent
// Differ instances and that a failed comparison leaves
// nothing behind for a retry.
type matchState struct {
	// comparisons counts old/new equality evaluations
	// performed by array matching: LCS table cells,
	// move/copy candidate verifications and
	// order-insensitive equivalence checks. Recursive
	// comparisons of aligned elements are not part of
	// array matching and are intentionally excluded.
	comparisons int

	lcsMemo map[eqLCSKey]eqResult
	eqMemo  map[eqPathKey]eqResult
	hashMem map[hashKey]uint64

	// prepared records the digest keys inserted into the
	// factorize hashmap during the current comparison, so
	// an aborted run can remove exactly the entries it
	// added without touching pre-existing shared state.
	prepared []uint64

	hook *matchHook
}

// resetMatch discards every matcher scratch value while
// retaining the optional test hook attached to the
// Differ. It is invoked at the start of each comparison
// and when a recoverable failure is unwound.
func (d *Differ) resetMatch() {
	hook := d.match.hook
	d.match = matchState{hook: hook}
}

// matchEqual is the single predicate used by array
// matching to compare an old value with a new one. It
// counts the evaluation, honors the memo of the current
// comparison so that each candidate pair is evaluated at
// most once, and remains the sole caller of deepEqual in
// the matching path. Hash collisions can therefore never
// be treated as equality on their own.
func (d *Differ) matchEqual(stage matchStage, key eqPathKey, old, new any) bool {
	return d.matchEval(stage, key, func() bool {
		return deepEqual(old, new)
	})
}

// matchIndexEqual is matchEqual for positional pairs
// compared by the LCS table.
func (d *Differ) matchIndexEqual(stage matchStage, key eqLCSKey, old, new any) bool {
	d.match.comparisons++
	if d.match.hook != nil && d.match.hook.abortAt == d.match.comparisons {
		panic(d.match.hook.abortError(stage))
	}
	if r, ok := d.match.lcsMemo[key]; ok {
		return r == eqTrue
	}
	r := eqFalse
	if deepEqual(old, new) {
		r = eqTrue
	}
	if d.match.lcsMemo == nil {
		d.match.lcsMemo = make(map[eqLCSKey]eqResult)
	}
	d.match.lcsMemo[key] = r
	return r == eqTrue
}

// matchEval runs an equality evaluation through the
// counter, abort hook and path-keyed memo.
func (d *Differ) matchEval(stage matchStage, key eqPathKey, eval func() bool) bool {
	d.match.comparisons++
	if d.match.hook != nil && d.match.hook.abortAt == d.match.comparisons {
		panic(d.match.hook.abortError(stage))
	}
	if r, ok := d.match.eqMemo[key]; ok {
		return r == eqTrue
	}
	r := eqFalse
	if eval() {
		r = eqTrue
	}
	if d.match.eqMemo == nil {
		d.match.eqMemo = make(map[eqPathKey]eqResult)
	}
	d.match.eqMemo[key] = r
	return r == eqTrue
}

// digestAt returns the stable digest of val at the given
// site, computing and memoizing it once per comparison.
// Digests are only used as cheap candidate hints: every
// digest hit is still verified through matchEqual before
// a value is treated as equal.
func (d *Differ) digestAt(site hashSite, val any) uint64 {
	if d.match.hook != nil {
		if h, ok := d.match.hook.forcedDigest(site); ok {
			return h
		}
	}
	if h, ok := d.match.hashMem[hashKey(site)]; ok {
		return h
	}
	h := d.hasher.digest(val, site.sorted)
	if d.match.hashMem == nil {
		d.match.hashMem = make(map[hashKey]uint64)
	}
	d.match.hashMem[hashKey(site)] = h
	return h
}

// arrayComparisonCount returns the number of old/new
// equality evaluations performed by array matching during
// the latest comparison. The counter is diagnostic-only:
// it is reset on every comparison and a fresh Differ has
// an isolated counter, so concurrent comparisons cannot
// observe each other.
func (d *Differ) arrayComparisonCount() int {
	return d.match.comparisons
}

// setMatcherHook attaches a package-internal test hook to
// the Differ. The hook shares the production matching and
// hashing code; it only forces digests at given sites or
// injects a single recoverable abort, it does not carry an
// alternative algorithm.
func (d *Differ) setMatcherHook(h *matchHook) {
	d.match.hook = h
}

// unorderedEqual reports whether src and tgt contain the
// same values when array order is ignored. Values are
// grouped by digest as a hint, then matched with
// matchEqual so that duplicates keep their multiplicity
// and distinct values sharing a digest cannot be paired.
func (d *Differ) unorderedEqual(scope string, src, tgt []any) bool {
	if len(src) != len(tgt) {
		return false
	}

	type candidate struct {
		path string
		val  any
		used bool
	}
	groups := make(map[uint64][]candidate, len(src))

	for i, v := range src {
		k := d.digestAt(hashSite{path: fmt.Sprintf("%s/%d", scope, i), sorted: true}, v)
		groups[k] = append(groups[k], candidate{
			path: fmt.Sprintf("%s/%d", scope, i),
			val:  v,
		})
	}
	for j, v := range tgt {
		k := d.digestAt(hashSite{path: fmt.Sprintf("%s/%d", scope, j), sorted: true}, v)
		group, ok := groups[k]
		if !ok {
			return false
		}
		bPath := fmt.Sprintf("%s/%d", scope, j)
		matched := false
		for i := range group {
			c := &group[i]
			if c.used {
				continue
			}
			if d.matchEqual(stageEquivalent, eqPathKey{
				scope: "equivalent",
				a:     c.path,
				b:     bPath,
			}, c.val, v) {
				c.used = true
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
