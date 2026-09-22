package jsondiff

import (
	"encoding/json"
	"strconv"
	"strings"
)

type pathTarget uint8

const (
	targetUnrelated pathTarget = iota
	targetLayout
	targetChild
)
func arrayRegionValid(prefix string, src, tgt []interface{}, ops []Operation) bool {
	cur := make([]interface{}, len(src))
	copy(cur, src)

	// classify interprets a path against the array. For
	// targetLayout it returns the layout index; for
	// targetChild it returns the index of the enclosing
	// element slot.
	classify := func(path string) (pathTarget, int) {
		if path != prefix && !hasProperPathPrefix(prefix, path) {
			return targetUnrelated, 0
		}
		rest := path
		if path != prefix {
			rest = path[len(prefix)+1:]
		}
		if rest == "" {
			return targetUnrelated, 0
		}
		seg := rest
		if i := strings.IndexByte(rest, separator); i >= 0 {
			seg = rest[:i]
		}
		n, err := strconv.Atoi(seg)
		if err != nil {
			return targetUnrelated, 0
		}
		if seg != rest {
			return targetChild, n
		}
		return targetLayout, n
	}

	// slot holds the provenance of a current element: when
	// true, the element originated at src[index] and its
	// final value will be compared to the target slot.
	provenance := make([]int, len(src))
	for i := range provenance {
		provenance[i] = i
	}

	for _, op := range ops {
		kind, idx := classify(op.Path)
		if kind == targetUnrelated {
			continue
		}
		if kind == targetChild {
			// Nested mutation: layout preserving, its
			// correctness is established by the recursive
			// diff that produced it.
			continue
		}

		switch op.Type {
		case OperationTest:
			continue
		case OperationReplace:
			if idx < 0 || idx >= len(cur) {
				return false
			}
			cur[idx] = op.Value
			provenance[idx] = -1
		case OperationAdd:
			if idx < 0 || idx > len(cur) {
				return false
			}
			cur = append(cur, nil)
			copy(cur[idx+1:], cur[idx:])
			cur[idx] = op.Value
			provenance = append(provenance, 0)
			copy(provenance[idx+1:], provenance[idx:])
			provenance[idx] = -1
		case OperationRemove:
			if idx < 0 || idx >= len(cur) {
				return false
			}
			cur = append(cur[:idx], cur[idx+1:]...)
			provenance = append(provenance[:idx], provenance[idx+1:]...)
		case OperationMove, OperationCopy:
			fromKind, fromIdx := classify(op.From)
			if fromKind == targetChild {
				// Moving/copying a child value between
				// element contents preserves every slot.
				continue
			}
			if fromKind != targetLayout || fromIdx < 0 || fromIdx >= len(cur) {
				return false
			}
			v := cur[fromIdx]
			origin := provenance[fromIdx]
			if op.Type == OperationMove {
				cur = append(cur[:fromIdx], cur[fromIdx+1:]...)
				provenance = append(provenance[:fromIdx], provenance[fromIdx+1:]...)
			}
			// The "path" index is evaluated on the document
			// resulting from the removal (RFC 6902).
			if idx < 0 || idx > len(cur) {
				return false
			}
			cur = append(cur, nil)
			copy(cur[idx+1:], cur[idx:])
			cur[idx] = v
			provenance = append(provenance, 0)
			copy(provenance[idx+1:], provenance[idx:])
			provenance[idx] = origin
		default:
			return false
		}
	}

	if len(cur) != len(tgt) {
		println("DBG len", len(cur), len(tgt))
		return false
	}
	for i := range cur {
		if !deepEqual(cur[i], tgt[i]) {
			cb, _ := jsonMarshal(cur[i]); tb, _ := jsonMarshal(tgt[i])
			println("DBG elem", i, string(cb), string(tb))
			return false
		}
	}
	return true
}

fu
