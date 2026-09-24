package jsondiff

// compareStats holds diagnostic counters that quantify the
// internal work performed while generating a patch.
//
// The counters are diagnostics only: they never influence
// the generated patch, its structure or the order of its
// operations. Every [Differ] owns a private instance, and
// the counters are reset at the beginning of each
// comparison, which guarantees that concurrent comparisons
// using different [Differ] values remain isolated.
type compareStats struct {
	// equalityComparisons counts invocations of deep
	// equality between an element of a source array and
	// an element of a target array. Hash comparisons are
	// not counted; the counter is incremented only when
	// the hashes agree and the actual values have to be
	// compared to guard against hash collisions.
	equalityComparisons int

	// digestComparisons counts invocations of the
	// cryptographic digest used to hash a JSON value.
	digestComparisons int

	// candidateLookups counts move/copy candidate probes
	// resolved against the unified candidate index.
	candidateLookups int
}

// reset clears every diagnostic counter.
func (s *compareStats) reset() {
	*s = compareStats{}
}

// stats returns a snapshot of the diagnostic counters
// collected by the last comparison performed by d.
//
// This is an internal accessor intended for diagnostics
// and behavior guards. It is unexported on purpose: the
// counters are not part of the public API surface and are
// shared by the exact same production code path that
// generates the patch; there is no test-only algorithm.
func (d *Differ) stats() compareStats {
	return d.compareStats
}
