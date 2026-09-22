package jsondiff

import (
	"encoding/binary"
	"hash/maphash"
	"math"
	"slices"
)

type hasher struct {
	mh maphash.Hash
}

func (h *hasher) digest(val interface{}, sort bool) uint64 {
	h.mh.Reset()
	h.hash(val, sort)

	return h.mh.Sum64()
}

func (h *hasher) hash(i interface{}, sort bool) {
	switch v := i.(type) {
	case string:
		_, _ = h.mh.WriteString(v)
	case bool:
		if v {
			_ = h.mh.WriteByte('1')
		} else {
			_ = h.mh.WriteByte('0')
		}
	case float64:
		var buf [8]byte
		binary.BigEndian.PutUint64(buf[:], math.Float64bits(v))
		_, _ = h.mh.Write(buf[:])
	case nil:
		_ = h.mh.WriteByte('0')
	case []interface{}:
		if sort {
			// Unordered equivalence hashes arrays as sorted
			// multisets at every nesting level.
			v = h.sortedElements(v, sort)
		}
		for _, e := range v {
			h.hash(e, sort)
		}
	case map[string]interface{}:
		keys := make([]string, 0, len(v))

		// Extract keys first, and sort them
		// in lexicographical order.
		for k := range v {
			keys = append(keys, k)
		}
		sortStrings(keys)

		for _, k := range keys {
			_, _ = h.mh.WriteString(k)
			h.hash(v[k], sort)
		}
	}
}

// sortedElements returns the array elements reordered by
// their digest, so that arrays differing only in order hash
// identically when sort is true.
func (h *hasher) sortedElements(a []interface{}, sort bool) []interface{} {
	h1 := hasher{}
	h2 := hasher{}

	h1.mh.SetSeed(h.mh.Seed())
	h2.mh.SetSeed(h.mh.Seed())

	// Sort a copy of the array: hashing must never mutate the
	// value being compared, which remains owned by the caller.
	cp := append([]interface{}(nil), a...)
	slices.SortStableFunc(cp, func(a1, a2 interface{}) int {
		d1 := h1.digest(a1, sort)
		d2 := h2.digest(a2, sort)

		if d1 > d2 {
			return 1
		} else if d1 < d2 {
			return -1
		}
		return 0
	})
	return cp
}
