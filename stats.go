package jsondiff

// A Stats collects diagnostic counters that quantify the
// comparison work performed by a Differ while generating a
// patch. The counters are purely informational; they have no
// effect on the generated patch.
type Stats struct {
	// Comparisons is the number of deep-equality comparisons
	// performed between JSON values.
	Comparisons int64

	// Digests is the number of hash digests computed for
	// JSON values.
	Digests int64
}
