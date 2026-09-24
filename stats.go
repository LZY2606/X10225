package jsondiff

// Stats contains diagnostic counters that describe the
// work performed by a Differ during its last comparison.
//
// The counters are purely informational: they never
// influence the content or the order of the generated
// patch. They are stored per Differ instance, which keeps
// concurrent comparisons isolated from each other, and
// they are reset at the beginning of each call to
// Differ.Compare and by Differ.Reset.
type Stats struct {
	// Equalities is the number of deep-equality
	// comparisons performed between JSON values.
	Equalities int
	// Hashes is the number of hash digests computed
	// to discover move/copy candidates and equivalent
	// values.
	Hashes int
	// Collisions is the number of candidate pairs that
	// shared the same hash digest but were rejected by
	// the deep-equality comparison. It proves that hash
	// collisions never result in mismatched values.
	Collisions int
}

// Stats returns the diagnostic counters collected by the
// Differ instance during its last comparison.
func (d *Differ) Stats() Stats {
	return d.stats
}

// equal compares two JSON values for deep equality and
// accounts the comparison in the diagnostic counters.
func (d *Differ) equal(src, tgt interface{}) bool {
	d.stats.Equalities++
	return deepEqual(src, tgt)
}

// digest computes the hash of a JSON value and accounts
// it in the diagnostic counters.
func (d *Differ) digest(v interface{}, sort bool) uint64 {
	d.stats.Hashes++
	return d.hasher.digest(v, sort)
}
