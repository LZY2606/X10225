package jsondiff

import "fmt"

// Stats holds diagnostic counters that describe the work
// performed by a Differ during its last comparison.
//
// The counters are purely informational: they never influence
// the generated patch, and they are kept per Differ instance,
// so concurrent comparisons made with distinct Differ values
// (including the top-level Compare and CompareJSON functions,
// which always use a fresh Differ) are fully isolated.
//
// The counters are reset at the beginning of each call to
// Differ.Compare and by Differ.Reset; a comparison that fails
// or is aborted therefore cannot leak counts into the stats
// of a subsequent comparison.
type Stats struct {
	// Digests is the number of hash digests computed while
	// matching array elements and searching move/copy
	// candidates.
	Digests uint64
	// Equalities is the number of deep equality checks
	// performed to confirm hash-matching candidates.
	Equalities uint64
	// Collisions is the number of candidate pairs whose
	// digests matched but whose values turned out to be
	// different after the deep equality check.
	Collisions uint64
}

// String implements the fmt.Stringer interface. The counters
// are always printed in a stable order.
func (s Stats) String() string {
	return fmt.Sprintf("digests=%d equalities=%d collisions=%d",
		s.Digests, s.Equalities, s.Collisions)
}
