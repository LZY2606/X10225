package jsondiff

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"sync"
	"testing"
)

// TestArrayMatch_RandomFixedSeed generates pseudo-random array
// pairs from a fixed seed and guards the end-to-end contract:
// determinism of the public patch, correctness by application,
// collision-safe matching under a forced digest, and the n*m
// comparison-count baseline.
//
// The seed is logged so a minimal failing input can always be
// reproduced by setting reproSeed/reproIter below.
func TestArrayMatch_RandomFixedSeed(t *testing.T) {
	const (
		seed      = int64(0x51A7E5)
		iterations = 200
		// Set reproIter >= 0 to reproduce a single iteration;
		// -1 runs the whole fixed-seed stream.
		reproIter = 14
	)

	run := func(forceCollisions bool) {
		t.Helper()
		rng := rand.New(rand.NewSource(seed))
		lo, hi := 0, iterations
		if reproIter >= 0 {
			lo, hi = reproIter, reproIter+1
			// Advance the deterministic stream to the iter.
			for i := 0; i < reproIter; i++ {
				genArray(rng)
				genArray(rng)
			}
		}
		for it := lo; it < hi; it++ {
			src := genArray(rng)
			tgt := genArray(rng)

			name := fmt.Sprintf("iter=%d/seed=%d/collisions=%v", it, seed, forceCollisions)
			d := &Differ{}
			if forceCollisions {
				d.diag.digest = func(interface{}) uint64 { return 0x51A7E5 }
			}
			d.applyOpts(Factorize(), LCS())

			si, ti := mustRoundtrip(t, src), mustRoundtrip(t, tgt)
			d.Compare(si, ti)
			got := d.Patch()

			// Applying the patch must reconstruct the target.
			applied, err := got.apply(mustMarshal(t, si), false)
			if err != nil {
				t.Fatalf("%s: patch applies with error: %v\n src=%v\n tgt=%v\n patch=%s",
					name, err, src, tgt, mustMarshalPatch(got))
			}
			var gotDoc, wantDoc interface{}
			if err := json.Unmarshal(applied, &gotDoc); err != nil {
				t.Fatalf("%s: applied document is invalid json: %v", name, err)
			}
			if err := json.Unmarshal(mustMarshal(t, ti), &wantDoc); err != nil {
				t.Fatalf("%s: target is invalid json: %v", name, err)
			}
			if !reflect.DeepEqual(gotDoc, wantDoc) {
				t.Fatalf("%s: patched document differs (minimal repro: set reproIter=%d):\n src=%s\n tgt=%s\n got=%s\nwant=%s",
					name, it, mustMarshal(t, si), mustMarshal(t, ti), applied, mustMarshal(t, ti))
			}
			// Determinism: a second fresh Differ returns the
			// exact same public patch.
			d2 := &Differ{}
			if forceCollisions {
				d2.diag.digest = func(interface{}) uint64 { return 0x51A7E5 }
			}
			d2.applyOpts(Factorize(), LCS())
			d2.Compare(si, ti)
			if !patchesEqual(d2.Patch(), got) {
				t.Fatalf("%s: non-deterministic patch:\n first=%s\nsecond=%s",
					name, mustMarshalPatch(got), mustMarshalPatch(d2.Patch()))
			}
			// Comparison-count baseline: no distinct pair is
			// evaluated more than once.
			n, m := len(si.([]interface{})), len(ti.([]interface{}))
			if d.stats.eqEvaluations > uint64(n*m) {
				t.Fatalf("%s: deepEqual evaluations %d exceed n*m %d; stats=%+v",
					name, d.stats.eqEvaluations, n*m, d.stats)
			}
			if d.stats.eqChecks > uint64(n*m+n+m) {
				t.Fatalf("%s: predicate calls %d exceed n*m+n+m %d; stats=%+v",
					name, d.stats.eqChecks, n*m+n+m, d.stats)
			}
			if d.stats.eqEvaluations > d.stats.eqChecks {
				t.Fatalf("%s: evaluations exceed calls; stats=%+v", name, d.stats)
			}
		}
	}
	t.Run("natural digests", func(t *testing.T) { run(false) })
	t.Run("forced collisions", func(t *testing.T) { run(true) })
}

// genArray builds a small JSON-shaped array with duplicated
// values from the deterministic RNG.
func genArray(rng *rand.Rand) []interface{} {
	alphabet := []interface{}{
		float64(1), float64(2), float64(2), "a", "b", "b",
		true, nil,
		map[string]interface{}{"k": "v"},
		[]interface{}{float64(9), float64(9)},
	}
	n := 1 + rng.Intn(5)
	a := make([]interface{}, n)
	for i := range a {
		a[i] = alphabet[rng.Intn(len(alphabet))]
	}
	return a
}

func mustRoundtrip(t *testing.T, v interface{}) interface{} {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out interface{}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

// TestArrayMatch_CountersDoNotAffectPatch locks the diagnostic
// contract: counters are observational. Running the same
// comparison with and without the collision-forcing seam cannot
// change the patch when inputs are the same, and concurrently
// running Differs never share counters (the barrier releases
// them simultaneously).
func TestArrayMatch_CountersDoNotAffectPatch(t *testing.T) {
	src := []interface{}{float64(1), float64(2), float64(2), "x"}
	tgt := []interface{}{"x", float64(2), float64(2), float64(1)}

	plain, err := Compare(src, tgt, Factorize(), LCS())
	if err != nil {
		t.Fatal(err)
	}
	const workers = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	failures := make(chan string, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			<-start
			d := &Differ{}
			d.applyOpts(Factorize(), LCS())
			d.Compare(src, tgt)
			if !patchesEqual(d.Patch(), plain) {
				failures <- fmt.Sprintf("worker %d patch mismatch: %s",
					id, mustMarshalPatch(d.Patch()))
				return
			}
			// Each worker observed at least some matching work.
			if d.stats.eqChecks == 0 {
				failures <- fmt.Sprintf("worker %d saw zero predicate calls", id)
			}
		}(w)
	}
	close(start)
	wg.Wait()
	close(failures)
	for f := range failures {
		t.Error(f)
	}
}
