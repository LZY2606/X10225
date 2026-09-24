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

func (h *hasher) digest(val interface{}, unordered bool) uint64 {
	h.mh.Reset()
	h.hash(val, unordered)

	return h.mh.Sum64()
}

// elementDigest hashes a value using a temporary hasher that
// shares the parent seed, without resetting the parent hash.
// It is used while hashing unordered arrays, where the
// parent stream must retain its already written bytes.
func (h *hasher) elementDigest(val interface{}, unordered bool) uint64 {
	var eh hasher
	eh.mh.SetSeed(h.mh.Seed())
	eh.hash(val, unordered)
	return eh.mh.Sum64()
}

func (h *hasher) hash(i interface{}, unordered bool) {
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
		// Prefix the element count so that arrays with
		// different lengths cannot hash like another.
		var lbuf [8]byte
		binary.BigEndian.PutUint64(lbuf[:], uint64(len(v)))
		_, _ = h.mh.Write(lbuf[:])

		if unordered {
			// Order-independent hashing: digest each
			// element (which keeps descending into nested
			// structures in unordered mode), sort the
			// digests and write them out. This never
			// mutates the input, unlike an in-place sort.
			digests := make([]uint64, len(v))
			for i, e := range v {
				digests[i] = h.elementDigest(e, true)
			}
			slices.Sort(digests)
			var dbuf [8]byte
			for _, d := range digests {
				binary.BigEndian.PutUint64(dbuf[:], d)
				_, _ = h.mh.Write(dbuf[:])
			}
			return
		}
		for _, e := range v {
			h.hash(e, false)
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
			h.hash(v[k], unordered)
		}
	}
}
