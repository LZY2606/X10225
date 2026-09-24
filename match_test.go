package jsondiff

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"testing"
)

// The tests in this file guard the contract of the unified
// array matching: hashing, equality, LCS and move/copy
// candidate discovery share a single computation, candidates
// are evaluated at most once, and the work is quantifiable
// through diagnostic counters.

// TestMatchingCases runs the fixed regression inputs that
// cover duplicate values, deep objects, head/tail insertions
// and cross moves, with move/copy discovery enabled.
func TestMatchingCases(t *testing.T) {
	runCasesFromFile(t, "testdata/tests/jsonpatch/matching/lcs_factorize.json", LCS(), Factorize())
}

// TestMatchingCases_DisabledOptimizations runs the same fixed
// inputs with all optimization options disabled, and guards
// that the differ falls back to the original semantics: no
// move or copy operations are generated.
func TestMatchingCases_DisabledOptimizations(t *testing.T) {
	runCasesFromFile(t, "testdata/tests/jsonpatch/matching/default.json")
}

// compareWithStats compares before and after through the
// public Differ entry point and returns the generated patch
// along with the diagnostic counters of the comparison.
func compareWithStats(t *testing.T, before, after interface{}, opts ...Option) (Patch, Stats) {
	t.Helper()

	afterBytes, err := json.Marshal(after)
	if err != nil {
		t.Fatal(err)
	}
	d := (&Differ{targetBytes: afterBytes}).WithOpts(opts...)
	d.Compare(before, after)

	return append(Patch{}, d.Patch()...), d.Stats()
}

// applyAndVerify applies the patch to the before document and
// checks that the result is identical to the after document.
func applyAndVerify(t *testing.T, patch Patch, before, after interface{}) {
	t.Helper()

	mustMarshal := func(v interface{}) []byte {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshaling error: %s", err)
		}
		return b
	}
	patched, err := patch.apply(mustMarshal(before), false)
	if err != nil {
		t.Fatalf("failed to apply patch: %s\npatch:\n%s", err, patch)
	}
	got, want := unmarshalMarshal(t, patched), unmarshalMarshal(t, mustMarshal(after))
	if !bytes.Equal(got, want) {
		t.Errorf("patched document mismatch:\n got: %s\nwant: %s\npatch:\n%s", got, want, patch)
	}
}

// countEqualPairs returns the number of pairs of deeply equal
// elements between src and tgt. It is the reference against
// which the number of equality checks performed by the array
// matcher is validated: each candidate pair must be evaluated
// exactly once.
func countEqualPairs(src, tgt []interface{}) int {
	var n int
	for i := range src {
		for j := range tgt {
			if deepEqual(src[i], tgt[j]) {
				n++
			}
		}
	}
	return n
}

// assertSamePatch fails the test at the first position where
// the got and want patches diverge, and reports the mismatch
// with its surrounding context.
func assertSamePatch(t *testing.T, got, want Patch) {
	t.Helper()

	if len(got) != len(want) {
		t.Errorf("patch length mismatch: got %d operations, want %d\ngot:\n%s\nwant:\n%s",
			len(got), len(want), got, want)
		return
	}
	for i := range got {
		g, w := got[i], want[i]
		if g.Type != w.Type || g.Path != w.Path || g.From != w.From ||
			!deepEqual(g.Value, w.Value) || !deepEqual(g.OldValue, w.OldValue) {
			t.Errorf("first mismatch at operation #%d:\n got: %s\nwant: %s\ncontext:\n%s",
				i, g, w, patchContext(got, i))
			return
		}
	}
}

// patchContext returns the string representation of the
// operations surrounding the operation at index idx.
func patchContext(p Patch, idx int) string {
	var sb strings.Builder

	lo, hi := max(0, idx-2), min(len(p), idx+3)
	for i := lo; i < hi; i++ {
		marker := "  "
		if i == idx {
			marker = ">>"
		}
		fmt.Fprintf(&sb, "%s #%d %s\n", marker, i, p[i])
	}
	return sb.String()
}

// TestMatchGuard_ComparisonBudget guards that the unified
// array matcher computes each candidate exactly once, and
// that the number of equality checks it performs never
// exceeds the baseline of the previous implementation, which
// systematically compared every pair of elements of the two
// arrays (len(src)*len(tgt) checks for the dynamic-programming
// table, plus up to len(src)+len(tgt) checks to backtrack it).
func TestMatchGuard_ComparisonBudget(t *testing.T) {
	deepDoc := func(arr []interface{}) map[string]interface{} {
		return map[string]interface{}{
			"a": map[string]interface{}{
				"b": map[string]interface{}{
					"c": map[string]interface{}{
						"d": arr,
						"e": "x",
					},
				},
			},
		}
	}
	for _, tc := range []struct {
		name         string
		before       interface{}
		after        interface{}
		srcArr       []interface{} // arrays matched by the LCS
		tgtArr       []interface{}
		wantMoveCopy bool // expect move/copy candidate discovery
	}{
		{
			name:         "duplicate values",
			before:       []interface{}{"a", "b", "a", "c", "a"},
			after:        []interface{}{"a", "a", "c", "a", "b"},
			srcArr:       []interface{}{"a", "b", "a", "c", "a"},
			tgtArr:       []interface{}{"a", "a", "c", "a", "b"},
			wantMoveCopy: true,
		},
		{
			name:         "deep objects",
			before:       deepDoc([]interface{}{1.0, 2.0, 3.0, 4.0}),
			after:        deepDoc([]interface{}{2.0, 3.0, 4.0, 5.0}),
			srcArr:       []interface{}{1.0, 2.0, 3.0, 4.0},
			tgtArr:       []interface{}{2.0, 3.0, 4.0, 5.0},
			wantMoveCopy: true,
		},
		{
			name:   "head insertion",
			before: []interface{}{2.0, 3.0, 4.0, 5.0},
			after:  []interface{}{1.0, 2.0, 3.0, 4.0, 5.0},
			srcArr: []interface{}{2.0, 3.0, 4.0, 5.0},
			tgtArr: []interface{}{1.0, 2.0, 3.0, 4.0, 5.0},
		},
		{
			name:   "tail insertion",
			before: []interface{}{1.0, 2.0, 3.0, 4.0},
			after:  []interface{}{1.0, 2.0, 3.0, 4.0, 5.0},
			srcArr: []interface{}{1.0, 2.0, 3.0, 4.0},
			tgtArr: []interface{}{1.0, 2.0, 3.0, 4.0, 5.0},
		},
		{
			name:   "head and tail insertion",
			before: []interface{}{2.0, 3.0, 4.0},
			after:  []interface{}{1.0, 2.0, 3.0, 4.0, 5.0},
			srcArr: []interface{}{2.0, 3.0, 4.0},
			tgtArr: []interface{}{1.0, 2.0, 3.0, 4.0, 5.0},
		},
		{
			name:         "cross move",
			before:       []interface{}{1.0, 2.0, 3.0, 4.0, 5.0, 6.0},
			after:        []interface{}{4.0, 5.0, 6.0, 1.0, 2.0, 3.0},
			srcArr:       []interface{}{1.0, 2.0, 3.0, 4.0, 5.0, 6.0},
			tgtArr:       []interface{}{4.0, 5.0, 6.0, 1.0, 2.0, 3.0},
			wantMoveCopy: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			patch, stats := compareWithStats(t, tc.before, tc.after, LCS(), Factorize())

			// The patch must be functionally correct.
			applyAndVerify(t, patch, tc.before, tc.after)

			n, m := len(tc.srcArr), len(tc.tgtArr)

			// The digest of each element is computed once.
			if want := n + m; stats.HashDigests != want {
				t.Errorf("hash digests: got %d, want %d (one per element)", stats.HashDigests, want)
			}
			// Each candidate pair of equal elements is
			// checked exactly once.
			wantChecks := countEqualPairs(tc.srcArr, tc.tgtArr)
			if stats.EqualChecks != wantChecks {
				t.Errorf("equality checks: got %d, want %d (one per equal pair)",
					stats.EqualChecks, wantChecks)
			}
			// Baseline threshold: the previous implementation
			// compared every pair of elements to fill the LCS
			// table, and compared pairs a second time while
			// backtracking it.
			oldBaseline := n*m + n + m
			if stats.EqualChecks > oldBaseline {
				t.Errorf("equality checks exceed old baseline: got %d, baseline %d",
					stats.EqualChecks, oldBaseline)
			}
			if got, want := stats.MoveCopyChecks > 0, tc.wantMoveCopy; got != want {
				t.Errorf("move/copy discovery checks: got %d, want discovery=%t",
					stats.MoveCopyChecks, want)
			}
			t.Logf("case %q: old baseline=[%d..%d] equality checks, new=%d, digests=%d, move/copy checks=%d",
				tc.name, n*m, oldBaseline, stats.EqualChecks, stats.HashDigests, stats.MoveCopyChecks)
		})
	}
}

// TestMatchGuard_NoArrayMatchingWithoutLCS guards that the
// diagnostic counters reflect that no array matching is
// performed when the LCS option is disabled.
func TestMatchGuard_NoArrayMatchingWithoutLCS(t *testing.T) {
	before := []interface{}{1.0, 2.0, 3.0}
	after := []interface{}{3.0, 2.0, 1.0}

	_, stats := compareWithStats(t, before, after)
	if stats.HashDigests != 0 || stats.EqualChecks != 0 {
		t.Errorf("expected no array matching counters without LCS, got %+v", stats)
	}
}

// useNumberUnmarshal unmarshals JSON documents with numbers
// preserved as json.Number values. All json.Number values
// share the same digest, which makes them a natural vector
// to exercise hash collisions through the public API.
func useNumberUnmarshal(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	return dec.Decode(v)
}

// TestMatchGuard_HashCollision guards that duplicate elements
// are never mismatched when their hashes collide: candidate
// pairs formed by the digest pre-filter are systematically
// verified with a deep-equality check.
func TestMatchGuard_HashCollision(t *testing.T) {
	unmarshalAll := func(b []byte) interface{} {
		t.Helper()
		var v interface{}
		if err := useNumberUnmarshal(b, &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	srcBytes := []byte(`[1,2,1,3]`)
	tgtBytes := []byte(`[3,1,2,1]`)
	before, after := unmarshalAll(srcBytes), unmarshalAll(tgtBytes)

	patch, stats := compareWithStats(t, before, after, LCS(), Factorize())

	// The patch must pair the duplicate numbers correctly:
	// applying it to the source must produce the target.
	applyAndVerify(t, patch, before, after)

	// All digests collide, so every pair of elements is a
	// candidate and is verified exactly once: the number of
	// equality checks equals the size of the LCS table, and
	// remains within the baseline of the old implementation,
	// which additionally re-compared pairs while backtracking.
	srcArr := before.([]interface{})
	tgtArr := after.([]interface{})
	if want := len(srcArr) * len(tgtArr); stats.EqualChecks != want {
		t.Errorf("equality checks with colliding hashes: got %d, want %d (each pair once)",
			stats.EqualChecks, want)
	}
	t.Logf("hash collision: old baseline=%d equality checks, new=%d",
		len(srcArr)*len(tgtArr)+len(srcArr)+len(tgtArr), stats.EqualChecks)

	// The public entry point accepts the same documents and
	// produces an identical patch.
	patchPublic, err := CompareJSON(srcBytes, tgtBytes,
		LCS(), Factorize(), UnmarshalFunc(useNumberUnmarshal))
	if err != nil {
		t.Fatal(err)
	}
	assertSamePatch(t, patchPublic, patch)
}

// TestMatchGuard_InvalidInputRejected guards that values of
// invalid JSON types are explicitly rejected through the
// existing error layer, repeatably, instead of being silently
// guessed as a successful comparison.
func TestMatchGuard_InvalidInputRejected(t *testing.T) {
	before := map[string]interface{}{"x": complex(1, 2)}
	after := map[string]interface{}{"x": complex(3, 4)}

	var errs []string
	for i := 0; i < 2; i++ {
		patch, err := CompareWithoutMarshal(before, after)
		if err == nil {
			t.Fatalf("expected non-nil error for invalid JSON type, got patch:\n%s", patch)
		}
		if patch != nil {
			t.Errorf("expected nil patch on error, got:\n%s", patch)
		}
		errs = append(errs, err.Error())
	}
	if errs[0] != errs[1] {
		t.Errorf("error is not repeatable: got %q then %q", errs[0], errs[1])
	}
	if want := "jsondiff: invalid json type: complex128"; errs[0] != want {
		t.Errorf("error mismatch: got %q, want %q", errs[0], want)
	}
}
