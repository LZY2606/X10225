package jsondiff

import (
	"encoding/binary"
	"hash/maphash"
	"math"
	"slices"
)

type hasher struct {
	mh   maphash.Hash
	hook digestHook
}

// digestHook is an internal test seam that allows forcing
// the digest of a value, in order to guard against
// incorrect behavior when distinct values collide.
// Production callers never set it, and a digest returned
// by the hook is still treated as a fast negative filter:
// equal digests always trigger a deep-equality check.
type digestHook func(interface{}) (uint64, bool)

func (h *hasher) digest(val interface{}, sort bool) uint64 {
	if h.hook != nil {
		if d, ok := h.hook(val); ok {
			return d
		}
	}
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
			h.sortArray(v)
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

func (h *hasher) sortArray(a []interface{}) {
	h1 := hasher{hook: h.hook}
	h2 := hasher{hook: h.hook}

	h1.mh.SetSeed(h.mh.Seed())
	h2.mh.SetSeed(h.mh.Seed())

	slices.SortStableFunc(a, func(a1, a2 interface{}) int {
		d1 := h1.digest(a1, true)
		d2 := h2.digest(a2, true)

		if d1 > d2 {
			return 1
		} else if d1 < d2 {
			return -1
		}
		return 0
	})
}
