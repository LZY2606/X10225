package jsondiff

// Stats holds diagnostic counters that describe the work
// performed by a Differ while matching the elements of
// arrays and discovering move/copy candidates.
//
// The counters are purely informational and have no effect
// on the generated patch. A Stats value is attached to a
// Differ with the WithStats option and is isolated to the
// comparisons performed by that Differ; distinct concurrent
// comparisons must use distinct Stats values.
type Stats struct {
	// HashDigests is the number of hash digests computed
	// for the candidate values of the compared documents.
	HashDigests int
	// DeepEqualComparisons is the number of deep equality
	// comparisons performed between candidate values.
	DeepEqualComparisons int
}

// WithStats returns an Option that attaches s to the Differ
// to collect diagnostic counters during the comparison.
// The counters are reset when the Differ is reset.
func WithStats(s *Stats) Option {
	return func(d *Differ) {
		d.stats = s
	}
}
