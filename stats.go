package jsondiff

// Stats holds diagnostic counters that quantify the work
// performed by a Differ while matching array elements and
// discovering move and copy candidates.
//
// The counters are strictly informational: they have no
// effect on the generated patch. They are owned by the
// Differ instance, thus concurrent comparisons performed
// by distinct Differ instances are isolated from each
// other. The counters are zeroed by Reset.
type Stats struct {
	// HashComputations counts the value digests computed
	// for array matching and move/copy candidate lookup.
	HashComputations uint64
	// EqualityComparisons counts the deep equality
	// comparisons performed to confirm digest matches.
	EqualityComparisons uint64
}

// Stats returns a copy of the diagnostic counters of the
// Differ instance. The result is only meaningful for the
// comparisons performed since the last Reset.
func (d *Differ) Stats() Stats {
	return d.stats
}
