package jsondiff

import "reflect"

// DiffStats contains diagnostic counters that describe the
// work performed by a Differ while matching array elements
// and discovering move/copy candidates.
//
// The counters are purely informational: they have no effect
// on the structure or the order of the generated patch. They
// are stored on the Differ instance itself, so concurrent
// comparisons performed with distinct Differ values (such as
// the ones created by Compare, CompareJSON and
// CompareWithoutMarshal) never share counts.
type DiffStats struct {
	// Equalities is the number of deep-equality comparisons
	// performed to confirm array element pairs and move/copy
	// candidates whose digests matched.
	Equalities int
	// Digests is the number of hash digests computed to
	// pre-filter array element pairs and move/copy candidates.
	Digests int
}

// Stats returns a copy of the diagnostic counters collected
// by the Differ. The counters are cumulative across calls to
// Compare, and are zeroed by Reset.
func (d *Differ) Stats() DiffStats {
	return d.stats
}

// equal reports whether src and tgt are deeply equal, and
// accounts the comparison in the diagnostic counters.
func (d *Differ) equal(src, tgt interface{}) bool {
	d.stats.Equalities++
	return deepEqual(src, tgt)
}

// digest returns the hash of v, and accounts it in the
// diagnostic counters.
func (d *Differ) digest(v interface{}, sort bool) uint64 {
	d.stats.Digests++
	return d.hasher.digest(v, sort)
}

// equalValue confirms the equality of two candidate values
// whose digests matched, and accounts the comparison in the
// diagnostic counters. Values that are not valid JSON types
// cannot be compared by deepEqual and are compared with
// reflect.DeepEqual instead.
func (d *Differ) equalValue(src, tgt interface{}) bool {
	d.stats.Equalities++
	if jsonTypeSwitch(src) == jsonInvalid || jsonTypeSwitch(tgt) == jsonInvalid {
		return reflect.DeepEqual(src, tgt)
	}
	return deepEqual(src, tgt)
}

// equalityMatrix stores the pairwise deep-equality of the
// elements of two slices as a bitset. Each pair is evaluated
// at most once, and the result is shared by the LCS dynamic
// programming table and its backtracking.
type equalityMatrix struct {
	bits   []uint64
	stride int
}

func newEqualityMatrix(n, m int) equalityMatrix {
	if n == 0 || m == 0 {
		return equalityMatrix{}
	}
	stride := (m + 63) / 64
	return equalityMatrix{
		bits:   make([]uint64, n*stride),
		stride: stride,
	}
}

func (m *equalityMatrix) set(i, j int) {
	m.bits[i*m.stride+j/64] |= 1 << (uint(j) % 64)
}

func (m *equalityMatrix) equal(i, j int) bool {
	return m.bits[i*m.stride+j/64]&(1<<(uint(j)%64)) != 0
}

// computeEquality evaluates the deep-equality of the elements
// of src and tgt. The digest of each element is computed a
// single time and used as a pre-filter, so that a pair is only
// ever compared when the digests of its elements match. Since
// equality is always confirmed by a deep comparison, duplicated
// values and hash collisions can never be mis-paired.
func (d *Differ) computeEquality(src, tgt []interface{}) equalityMatrix {
	m := newEqualityMatrix(len(src), len(tgt))
	if m.stride == 0 {
		return m
	}
	// Group the indices of the target elements by digest.
	groups := make(map[uint64][]int, len(tgt))
	for j, tv := range tgt {
		k := d.digest(tv, false)
		groups[k] = append(groups[k], j)
	}
	for i, sv := range src {
		k := d.digest(sv, false)
		for _, j := range groups[k] {
			if d.equal(sv, tgt[j]) {
				m.set(i, j)
			}
		}
	}
	return m
}

// removalIndex tracks the patch positions of the remove
// operations generated during a comparison, indexed by the
// digest of their old value. It allows move candidates to be
// discovered without scanning and re-comparing the whole patch
// for every addition.
type removalIndex struct {
	byDigest map[uint64][]int
}

// register records that the remove operation at patch position
// idx removed a value with the given digest.
func (r *removalIndex) register(digest uint64, idx int) {
	if r.byDigest == nil {
		r.byDigest = make(map[uint64][]int)
	}
	r.byDigest[digest] = append(r.byDigest[digest], idx)
}

// candidates returns the patch positions of the registered
// remove operations whose old value shares the given digest,
// in ascending patch order.
func (r *removalIndex) candidates(digest uint64) []int {
	return r.byDigest[digest]
}

// unregister removes the operation at patch position idx from
// the candidates of the given digest.
func (r *removalIndex) unregister(digest uint64, idx int) {
	idxs := r.byDigest[digest]
	for n, i := range idxs {
		if i == idx {
			r.byDigest[digest] = append(idxs[:n], idxs[n+1:]...)
			return
		}
	}
}

// shift adjusts the tracked positions by delta to account for
// an operation inserted into or removed from the patch at
// position pos.
func (r *removalIndex) shift(pos, delta int) {
	for k, idxs := range r.byDigest {
		for n, i := range idxs {
			if i >= pos {
				idxs[n] = i + delta
			}
		}
		r.byDigest[k] = idxs
	}
}

// dropFrom unregisters all operations at position pos and
// beyond, to account for a truncation of the patch.
func (r *removalIndex) dropFrom(pos int) {
	for k, idxs := range r.byDigest {
		kept := idxs[:0]
		for _, i := range idxs {
			if i < pos {
				kept = append(kept, i)
			}
		}
		r.byDigest[k] = kept
	}
}
