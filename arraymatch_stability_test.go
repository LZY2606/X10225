package jsondiff

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
)

// These cases only perturb execution ordering, scheduling or
// cache state; the public patch structure and stable order must
// remain identical.

func canonical(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// runThroughPublicEntry exercises every formal public entry.
func runThroughPublicEntry(t *testing.T, entry string, src, tgt any, opts ...Option) Patch {
	t.Helper()
	sb, _ := json.Marshal(src)
	tb, _ := json.Marshal(tgt)
	switch entry {
	case "Compare":
		p, err := Compare(src, tgt, opts...)
		if err != nil {
			t.Fatalf("Compare: %s", err)
		}
		return p
	case "CompareJSON":
		p, err := CompareJSON(sb, tb, opts...)
		if err != nil {
			t.Fatalf("CompareJSON: %s", err)
		}
		return p
	case "CompareWithoutMarshal":
		p, err := CompareWithoutMarshal(canonical(t, src), canonical(t, tgt), opts...)
		if err != nil {
			t.Fatalf("CompareWithoutMarshal: %s", err)
		}
		return p
	default:
		t.Fatalf("unknown public entry %q", entry)
		return nil
	}
}

// TestStability_AcrossEntriesAndOrder reruns the same input through
// all public entries, then repeatedly with cache state present and
// absent; the order-sensitive summary must never change.
func TestStability_AcrossEntriesAndOrder(t *testing.T) {
	src := map[string]any{
		"list": []any{"a", "b", "c", "d"},
	}
	tgt := map[string]any{
		"list": []any{"d", "a", "c", "b", "e"},
	}
	opts := []Option{LCS(), Factorize(), Equivalent(), Invertible()}

	var baseline []opSummary
	for _, entry := range []string{"Compare", "CompareJSON", "CompareWithoutMarshal"} {
		got := summarize(runThroughPublicEntry(t, entry, src, tgt, opts...))
		if baseline == nil {
			baseline = got
			continue
		}
		if !equalSummaries(baseline, got) {
			t.Fatalf("entry %s changed patch structure:\n got  %v\n want %v", entry, got, baseline)
		}
	}

	// Repeated runs with fresh instances (no retained cache) and
	// varying scheduling cannot perturb output.
	for iter := 0; iter < 5; iter++ {
		d := &Differ{}
		d.WithOpts(opts...)
		d.Compare(canonical(t, src), canonical(t, tgt))
		if got := summarize(d.Patch()); !equalSummaries(baseline, got) {
			t.Fatalf("iteration %d changed order:\n got  %v\n want %v", iter, got, baseline)
		}
	}
}

// TestStability_EnumPerturbation changes only result-irrelevant
// conditions: option construction order and diagnostic hooks.
func TestStability_EnumPerturbation(t *testing.T) {
	src := []any{"a", "b", "c"}
	tgt := []any{"c", "b", "a"}

	p1, _ := mustPatch(t, src, tgt, LCS(), Factorize())
	p2, _ := mustPatch(t, src, tgt, Factorize(), LCS())
	// A collision-free digest hook changes scheduling of hashing
	// (real hasher replaced by a type-tag-consistent stub) but not
	// any result.
	stubHasher := hasher{}
	d := &Differ{}
	d.digestHook = func(v any) uint64 {
		// Distinct per distinct value, constant per value.
		return stubHasher.digest(v, false)
	}
	d.WithOpts(LCS(), Factorize())
	d.Compare(src, tgt)
	p3 := d.Patch()

	w := summarize(p1)
	for i, p := range []Patch{p2, p3} {
		if got := summarize(p); !equalSummaries(w, got) {
			t.Fatalf("perturbation %d changed stable order:\n got  %v\n want %v", i, got, w)
		}
	}
}

// injectedFault is a sentinel recoverable mid-comparison error.
type injectedFault struct{ stage string }

func (e *injectedFault) Error() string { return "injected fault at " + e.stage }

// TestStability_RecoverableMidFault injects a failure after the
// array matching plan is built but before emission. The patch
// visible before the failure must be untouched by the aborted
// array; shared state must not be half-updated; a retry with the
// identical input must produce the stable result and carry no
// leftover cache/counters/temp state.
func TestStability_RecoverableMidFault(t *testing.T) {
	src := []any{"a", "b", "c"}
	tgt := []any{"c", "b", "a"}

	failOnce := true
	d := &Differ{}
	d.WithOpts(LCS(), Factorize())
	d.matcherFault = func([]interface{}, []interface{}) {
		if failOnce {
			failOnce = false
			panic(&injectedFault{stage: "post-plan/pre-emit"})
		}
	}

	// The array sorts before other object keys, so place the
	// stable prefix under a lexicographically earlier key.
	docSrc := map[string]any{"a_head": float64(1), "arr": src}
	docTgt := map[string]any{"a_head": float64(2), "arr": tgt}

	func() {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("expected injected fault")
			}
			if _, ok := r.(*injectedFault); !ok {
				t.Fatalf("unexpected panic type %T: %v", r, r)
			}
		}()
		d.Compare(docSrc, docTgt)
	}()

	// After recovery, the aborted array contributed nothing; the
	// only visible op is the stable head replacement.
	partial := summarize(d.Patch())
	wantPartial := []opSummary{{OperationReplace, "/a_head", ""}}
	if !equalSummaries(partial, wantPartial) {
		t.Fatalf("half-updated patch after fault:\n got  %v\n want %v", partial, wantPartial)
	}

	// Retry with the identical input after Reset: result must be
	// stable and free of leftover candidates/counters.
	d.Reset()
	d.Compare(docSrc, docTgt)
	retry := d.Patch()
	assertPatchApplies(t, docSrc, retry, docTgt)

	// A subsequent unrelated Compare must not observe counters or
	// ledger from the retried/failed runs.
	d.Reset()
	d.Compare(map[string]any{"k": float64(1)}, map[string]any{"k": float64(1)})
	if len(d.Patch()) != 0 {
		t.Fatalf("no-op compare produced ops after failure: %v", d.Patch())
	}
	if len(d.removals.entries) != 0 {
		t.Fatalf("ledger leaked across comparisons: %v", d.removals.entries)
	}
	if st := d.MatcherStats(); st.MoveCandidates != 0 || st.CopyCandidates != 0 || st.Matchers != 0 {
		t.Fatalf("counters leaked across comparisons: %+v", st)
	}
}

// TestStability_ConcurrentIsolation runs independent Differs
// concurrently behind a barrier so they overlap maximally. Each
// Differ owns its counters, ledger and matcher; results and
// diagnostic counts must be exactly those of serial runs.
func TestStability_ConcurrentIsolation(t *testing.T) {
	src := []any{"a", "b", "c", "d"}
	tgt := []any{"d", "c", "b", "a"}

	const n = 24
	var wg sync.WaitGroup
	barrier := make(chan struct{})
	errs := make(chan error, n)
	wg.Add(n)
	for g := 0; g < n; g++ {
		go func(id int) {
			defer wg.Done()
			<-barrier // release all goroutines together
			d := &Differ{}
			d.WithOpts(LCS(), Factorize())
			d.Compare(src, tgt)
			p := d.Patch()
			if len(p) == 0 {
				errs <- errors.New("empty patch from concurrent compare")
				return
			}
			// Each instance must observe a private, fully
			// populated counter set.
			st := d.MatcherStats()
			if st.Matchers == 0 || st.EqualDeep == 0 {
				errs <- errors.New("isolated counters missing")
				return
			}
			// The patch must apply against a private source copy.
			sb, _ := json.Marshal(src)
			out, err := p.apply(sb, false)
			if err != nil {
				errs <- err
				return
			}
			tb, _ := json.Marshal(tgt)
			if string(out) != string(tb) {
				errs <- errors.New("concurrent patch mismatch")
			}
		}(g)
	}
	close(barrier)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

// TestStability_UnsupportedIsExplicit verifies that unrepresentable
// input is rejected through the existing error layering, never
// guessed as success, and the rejection is repeatable.
func TestStability_UnsupportedIsExplicit(t *testing.T) {
	type custom struct{ N int }
	for i := 0; i < 2; i++ {
		_, err := CompareWithoutMarshal(custom{1}, custom{2}, LCS(), Factorize())
		if err == nil {
			t.Fatalf("run %d: unsupported input reported as success", i)
		}
		// Existing error contract: jsondiff-prefixed invalid type.
		if !contains(err.Error(), "jsondiff: invalid json type") {
			t.Fatalf("run %d: rejection does not match error layering: %v", i, err)
		}
	}
}

func equalSummaries(a, b []opSummary) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
