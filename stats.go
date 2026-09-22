package jsondiff

// matchStats holds diagnostic counters for a single
// top-level comparison. Counters are never consulted by
// the diff algorithm itself: they do not influence the
// generated patch, its structure or its ordering.
//
// A fresh matchStats is allocated for every comparison,
// so counters of concurrent comparisons performed on
// distinct Differ instances are fully isolated and the
// counters never survive a comparison (successful or
// not) on the Differ instance.
type matchStats struct {
	// elementCompares counts old/new array element
	// pairs whose equality is actually evaluated by
	// deep equality. A pair is evaluated at most once
	// per array comparison.
	elementCompares int
	// hashCompares counts old/new element pairs that
	// are compared through their content hash.
	hashCompares int
	// hashCollisions counts pairs of distinct elements
	// sharing the same digest hash; each such pair is
	// always resolved by deep equality.
	hashCollisions int
	// moveCandidates counts removed-element lookups
	// performed while discovering move operations.
	moveCandidates int
	// copyCandidates counts unchanged-location lookups
	// performed while discovering copy operations.
	copyCandidates int
	// matchers counts the number of array matching
	// phases performed during the comparison.
	matchers int
	// aborted reports whether the comparison was
	// interrupted before completion.
	aborted bool
}

// snapshot returns a copy of the stats. Counters are
// copied by value so that callers cannot mutate the
// diagnostics captured during a comparison.
func (s matchStats) snapshot() matchStats {
	return s
}
