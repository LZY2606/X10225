package jsondiff

import (
	"encoding/json"
	"testing"
)

// TestArrayMatch_HashCollision guards the invariant that
// digests only partition candidates: every equal-digest pairing
// must be confirmed by deepEqual, so duplicated values and
// forced digest collisions cannot pair unequal elements.
//
// The collision is forced through the unexported diagnostic
// digest seam, which routes every digest of the production
// matching pipeline to the test function; no matching algorithm
// is duplicated for the test.
func TestArrayMatch_HashCollision(t *testing.T) {
	src := []interface{}{"alpha", "bravo", "alpha", float64(7)}
	tgt := []interface{}{"alpha", "alpha", "charlie", float64(8)}

	// Every digest collides; only values that are actually
	// deeply equal may ever be paired.
	forced := 0
	d := &Differ{
		diag: diagHooks{
			digest: func(interface{}) uint64 {
				forced++
				return 1
			},
		},
	}
	d.applyOpts(Factorize(), LCS())
	patch, err := compare(d, src, tgt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := Patch{
		op(OperationRemove, "/1"),
		opVal(OperationReplace, "/3", float64(8)),
		opVal(OperationAdd, "/2", "charlie"),
	}
	if !patchesEqual(patch, want) {
		t.Fatalf("collision changed pairing:\n got %s\nwant %s\n(first offending pairing is the move/remove of /1)",
			mustMarshalPatch(patch), mustMarshalPatch(want))
	}
	// The patch must still transform source into target.
	applied, err := patch.apply(mustMarshal(t, src), false)
	if err != nil {
		t.Fatalf("patch fails to apply: %v", err)
	}
	var gotDoc, wantDoc interface{}
	_ = json.Unmarshal(applied, &gotDoc)
	_ = json.Unmarshal(mustMarshal(t, tgt), &wantDoc)
	if !reflectDeep(gotDoc, wantDoc) {
		t.Fatalf("patched document mismatch:\n got %s\nwant %s", applied, mustMarshal(t, tgt))
	}
	if d.stats.collisions == 0 {
		t.Fatalf("forced collision scenario recorded zero rejected collisions; stats=%+v", d.stats)
	}
	// Every distinct (i,j) pair inside the colliding bucket is
	// evaluated at most once.
	if d.stats.eqEvaluations > d.stats.eqChecks {
		t.Fatalf("evaluations %d exceed predicate calls %d; stats=%+v",
			d.stats.eqEvaluations, d.stats.eqChecks, d.stats)
	}
	upper := uint64(len(src) * len(tgt))
	if d.stats.eqChecks > upper {
		t.Fatalf("predicate calls %d exceed n*m baseline %d; stats=%+v",
			d.stats.eqChecks, upper, d.stats)
	}
	t.Logf("forced digest calls=%d stats=%+v", forced, d.stats)
}

// reflectDeep is a small wrapper kept local to this file.
func reflectDeep(a, b interface{}) bool { return deepEqual(a, b) }
