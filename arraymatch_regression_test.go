package jsondiff

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

// This file is the standalone regression material for
// "jsondiff: unify array matching and move/copy candidate
// accounting".
//
// Every case enters through a formal public entry point
// (Compare, CompareJSON or CompareWithoutMarshal). Tests do
// not assemble internal patch objects; they assert on the
// operations observed by callers, on the correctness of the
// patch through application, and on the internal invariants of
// the matching pipeline (stable ordering, collision-safe
// pairing, candidates evaluated once, quantifiable comparison
// counts and isolation of diagnostic state).

// arrayGuardCase is a fixed counterexample entering the public
// Compare entry point.
type arrayGuardCase struct {
	name    string
	src     interface{}
	tgt     interface{}
	opts    []Option
	want    Patch // exact public operations seen by the caller
	maxEval uint64
	// maxProbe bounds move-candidate predicate calls.
	maxProbe uint64
	// noMove/noCopy forbid the corresponding operation types.
	noMove bool
	noCopy bool
}

func op(typ, path string) Operation { return Operation{Type: typ, Path: path} }
func opFrom(typ, from, path string) Operation {
	return Operation{Type: typ, From: from, Path: path}
}
func opVal(typ, path string, v interface{}) Operation {
	return Operation{Type: typ, Path: path, Value: v}
}

// runGuard drives a case through the public Compare entry
// point, validates the exact operations, applies the patch to
// the source document, and checks the comparison-count budget
// and internal invariants.
func runGuard(t *testing.T, tc arrayGuardCase) matchStats {
	t.Helper()

	got, err := Compare(tc.src, tc.tgt, tc.opts...)
	if err != nil {
		t.Fatalf("%s: Compare returned unexpected error: %v", tc.name, err)
	}
	// First offending location and the necessary context are
	// reported, never a bare pass/fail.
	if len(got) != len(tc.want) {
		t.Fatalf("%s: operation count mismatch at #0:\n got %s\nwant %s",
			tc.name, mustMarshalPatch(got), mustMarshalPatch(tc.want))
	}
	for i := range tc.want {
		g, w := got[i], tc.want[i]
		if g.Type != w.Type {
			t.Fatalf("%s: op #%d type mismatch: got %q, want %q\n got %s\nwant %s",
				tc.name, i, g.Type, w.Type, mustMarshalPatch(got), mustMarshalPatch(tc.want))
		}
		if g.Path != w.Path {
			t.Fatalf("%s: op #%d path mismatch: got %q, want %q\n got %s\nwant %s",
				tc.name, i, g.Path, w.Path, mustMarshalPatch(got), mustMarshalPatch(tc.want))
		}
		if (w.Type == OperationMove || w.Type == OperationCopy) && g.From != w.From {
			t.Fatalf("%s: op #%d from mismatch: got %q, want %q\n got %s\nwant %s",
				tc.name, i, g.From, w.From, mustMarshalPatch(got), mustMarshalPatch(tc.want))
		}
		if (w.Type == OperationAdd || w.Type == OperationReplace || w.Type == OperationTest) &&
			!reflect.DeepEqual(g.Value, w.Value) {
			t.Fatalf("%s: op #%d value mismatch: got %v, want %v",
				tc.name, i, g.Value, w.Value)
		}
	}
	// The patch must remain a valid transformation.
	applied, err := got.apply(mustMarshal(t, tc.src), false)
	if err != nil {
		t.Fatalf("%s: generated patch fails to apply: %v", tc.name, err)
	}
	wantDoc := mustMarshal(t, tc.tgt)
	gotDoc := mustMarshal(t, json.RawMessage(applied))
	if !bytes.Equal(canonJSON(t, gotDoc), canonJSON(t, wantDoc)) {
		t.Fatalf("%s: patch does not produce the target:\n got %s\nwant %s",
			tc.name, applied, wantDoc)
	}

	// Re-run through a fresh Differ to read the diagnostic
	// counters; the public patch must be byte-identical.
	d := &Differ{}
	d.applyOpts(tc.opts...)
	p2, err := compare(d, tc.src, tc.tgt)
	if err != nil || !patchesEqual(got, p2) {
		t.Fatalf("%s: non-deterministic patch across comparisons: %v", tc.name, err)
	}
	st := d.stats

	// Quantifiable old/new comparison budget:
	//   - every distinct (i,j) pair is evaluated with
	//     deepEqual at most once: eqEvaluations <= n*m;
	//   - predicate calls also include memoized backtracking
	//     visits, bounded by n*m + n + m.
	n, m := jsonLen(tc.src), jsonLen(tc.tgt)
	if st.eqEvaluations > uint64(n*m) {
		t.Fatalf("%s: deepEqual evaluations %d exceed the n*m baseline %d; stats=%+v",
			tc.name, st.eqEvaluations, n*m, st)
	}
	predicateBaseline := uint64(n*m + n + m)
	if st.eqChecks > predicateBaseline {
		t.Fatalf("%s: equality predicate calls %d exceed baseline n*m+n+m=%d; stats=%+v",
			tc.name, st.eqChecks, predicateBaseline, st)
	}
	if tc.maxEval > 0 && st.eqEvaluations > tc.maxEval {
		t.Fatalf("%s: deepEqual evaluations %d exceed guarded baseline %d; stats=%+v",
			tc.name, st.eqEvaluations, tc.maxEval, st)
	}
	if tc.maxProbe > 0 && st.moveProbes > tc.maxProbe {
		t.Fatalf("%s: move probes %d exceed guarded baseline %d; stats=%+v",
			tc.name, st.moveProbes, tc.maxProbe, st)
	}
	if st.eqEvaluations > st.eqChecks {
		t.Fatalf("%s: evaluations (%d) cannot exceed predicate calls (%d); stats=%+v",
			tc.name, st.eqEvaluations, st.eqChecks, st)
	}
	for _, o := range got {
		if tc.noMove && o.Type == OperationMove {
			t.Fatalf("%s: move operation emitted while optimization disabled: %s",
				tc.name, mustMarshalPatch(got))
		}
		if tc.noCopy && o.Type == OperationCopy {
			t.Fatalf("%s: copy operation emitted while optimization disabled: %s",
				tc.name, mustMarshalPatch(got))
		}
	}
	t.Logf("%s: stats=%+v patch=%s", tc.name, st, mustMarshalPatch(got))
	return st
}

func jsonLen(v interface{}) int {
	switch a := v.(type) {
	case []interface{}:
		return len(a)
	}
	return 0
}

func mustMarshal(t *testing.T, v interface{}) []byte {
	t.Helper()
	if raw, ok := v.(json.RawMessage); ok {
		var buf bytes.Buffer
		if err := json.Compact(&buf, raw); err != nil {
			t.Fatalf("compact json: %v", err)
		}
		return buf.Bytes()
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func canonJSON(t *testing.T, b []byte) []byte {
	t.Helper()
	var v interface{}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("unmarshal %s: %v", b, err)
	}
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return out
}

func mustMarshalPatch(p Patch) string {
	b, err := json.Marshal(p)
	if err != nil {
		return fmt.Sprintf("<invalid patch: %v>", err)
	}
	return string(b)
}

func patchesEqual(a, b Patch) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return bytes.Equal(ab, bb)
}

// TestArrayMatch_Guards is the fixed set of minimal inputs that
// progressively exercise duplicated values, deep objects,
// head/tail insertions, crossing moves and disabled
// move/copy optimizations.
func TestArrayMatch_Guards(t *testing.T) {
	deep1 := map[string]interface{}{"k": map[string]interface{}{"n": float64(1)}}
	deep2 := map[string]interface{}{"k": map[string]interface{}{"n": float64(2)}}

	cases := []arrayGuardCase{{
		name: "duplicated values pair by multiplicity not hash set",
		src:  []interface{}{float64(1), float64(1), float64(2), float64(2)},
		tgt:  []interface{}{float64(2), float64(2), float64(1), float64(1)},
		opts: []Option{Factorize(), LCS()},
		want: Patch{
			opFrom(OperationMove, "/0", "/3"),
			opFrom(OperationMove, "/0", "/2"),
		},
		maxEval:  8,
		maxProbe: 4,
	}, {
		name: "crossing move over deep equal objects",
		src:  []interface{}{deep1, "q"},
		tgt:  []interface{}{"q", deep1},
		opts: []Option{Factorize(), LCS()},
		want: Patch{
			opFrom(OperationMove, "/0", "/1"),
		},
		maxEval: 2,
	}, {
		name: "deep unequal objects must not be paired as a move",
		src:  []interface{}{deep1, "q"},
		tgt:  []interface{}{"q", deep2},
		opts: []Option{Factorize(), LCS()},
		want: Patch{
			op(OperationRemove, "/0"),
			opVal(OperationAdd, "/1", map[string]interface{}{
				"k": map[string]interface{}{"n": float64(2)},
			}),
		},
		noMove: true,
	}, {
		name: "head insertion keeps tail elements in place",
		src:  []interface{}{"x", "y"},
		tgt:  []interface{}{"z", "x", "y"},
		opts: []Option{Factorize(), LCS()},
		want: Patch{
			opVal(OperationAdd, "/0", "z"),
		},
		maxEval: 2,
	}, {
		name: "tail insertion appends at the end",
		src:  []interface{}{"x", "y"},
		tgt:  []interface{}{"x", "y", "z"},
		opts: []Option{Factorize(), LCS()},
		want: Patch{
			opVal(OperationAdd, "/2", "z"),
		},
		maxEval: 2,
	}, {
		name: "rotation crossing move factorizes into add and remove",
		src:  []interface{}{"a", "b", "c"},
		tgt:  []interface{}{"c", "a", "b"},
		opts: []Option{Factorize(), LCS()},
		want: Patch{
			opVal(OperationAdd, "/0", "c"),
			op(OperationRemove, "/3"),
		},
		maxEval: 3,
	}, {
		name:   "duplicated values without LCS fall back to positional replaces",
		src:    []interface{}{float64(1), float64(1), float64(2), float64(2)},
		tgt:    []interface{}{float64(2), float64(2), float64(1), float64(1)},
		opts:   []Option{Factorize()},
		want:   positionalReplaces([]interface{}{float64(2), float64(2), float64(1), float64(1)}),
		noMove: true,
		maxEval: 0,
	}, {
		name:   "crossing move disabled without factorization uses replaces",
		src:    []interface{}{"a", "b", "c"},
		tgt:    []interface{}{"c", "a", "b"},
		opts:   nil,
		want:   positionalReplaces([]interface{}{"c", "a", "b"}),
		noMove: true,
		noCopy: true,
		maxEval: 0,
	}, {
		name: "LCS without factorization emits removes then adds, no move/copy",
		src:  []interface{}{float64(1), float64(1), float64(2), float64(2)},
		tgt:  []interface{}{float64(2), float64(2), float64(1), float64(1)},
		opts: []Option{LCS()},
		want: Patch{
			op(OperationRemove, "/0"),
			op(OperationRemove, "/0"),
			opVal(OperationAdd, "/2", float64(1)),
			opVal(OperationAdd, "/3", float64(1)),
		},
		noMove: true,
		noCopy: true,
		maxEval: 8,
	}, {
		name: "invertible arrays never use copy operations",
		src:  []interface{}{"foo", "bar"},
		tgt:  []interface{}{"foo", "bar", "foo"},
		opts: []Option{Factorize(), LCS(), Invertible()},
		want: Patch{
			opVal(OperationAdd, "/2", "foo"),
		},
		noCopy: true,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runGuard(t, tc)
		})
	}
}

func positionalReplaces(vals []interface{}) Patch {
	p := make(Patch, 0, len(vals))
	for i, v := range vals {
		p = append(p, opVal(OperationReplace, fmt.Sprintf("/%d", i), v))
	}
	return p
}
