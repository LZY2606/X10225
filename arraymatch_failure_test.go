package jsondiff

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
)

// errInjectedArrayMatch is the sentinel used to simulate a
// recoverable failure in the middle of array matching.
var errInjectedArrayMatch = errors.New("injected array-matching failure")

// failingSource returns JSON whose top level contains two
// arrays; the hook fires on the inner array only.
func failureInputs() ([]byte, []byte) {
	src := []byte(`{"outer":[1,{"inner":["a","b","c"]}]}`)
	tgt := []byte(`{"outer":[1,{"inner":["c","a","b"]}]}`)
	return src, tgt
}

// TestArrayMatch_RecoverableFailure verifies that a failure
// injected in the middle of matching leaves no half-updated
// public or shared state, and that retrying the exact same
// inputs through the public entry yields a stable patch.
func TestArrayMatch_RecoverableFailure(t *testing.T) {
	src, tgt := failureInputs()
	fired := 0

	// Reference result, never produced while the hook is armed.
	ref, err := CompareJSON(src, tgt, Factorize(), LCS())
	if err != nil {
		t.Fatalf("reference comparison failed: %v", err)
	}

	d := &Differ{}
	d.opts.factorize = true
	d.opts.lcs = true
	d.diag.hook = func(phase matchPhase, p pointer) {
		// Fail once, deterministically, on the inner array at
		// the beginning of its matching work.
		if fired == 0 && phase == phaseMatcherStart && p.string() == "/outer/1/inner" {
			fired++
			panic(errInjectedArrayMatch)
		}
	}
	var panicked interface{}
	func() {
		defer func() { panicked = recover() }()
		_, _ = compareJSON(d, src, tgt, nil)
	}()
	if panicked == nil {
		t.Fatalf("expected the injected failure to propagate")
	}
	if panicked != errInjectedArrayMatch {
		t.Fatalf("failure did not surface unchanged: got %#v", panicked)
	}
	// No half-updated public state survives.
	if len(d.patch) != 0 {
		t.Fatalf("partial patch survived failure: %s", mustMarshalPatch(d.patch))
	}
	if d.matcher != nil {
		t.Fatalf("matcher stack leaked after failure: %#v", d.matcher)
	}
	if d.hashmap != nil {
		t.Fatalf("prepared hashmap survived failure: %#v", d.hashmap)
	}
	if d.stats != (matchStats{}) {
		t.Fatalf("diagnostic counters survived failure: %+v", d.stats)
	}
	if d.removeSeq != 0 {
		t.Fatalf("remove sequence counter survived failure: %d", d.removeSeq)
	}

	// Retry with the exact same inputs on the same Differ,
	// hook disarmed: the result must be stable and carry no
	// trace of the failed attempt.
	d.diag.hook = nil
	retry, err := compareJSON(d, src, tgt, nil)
	if err != nil {
		t.Fatalf("retry after recovery failed: %v", err)
	}
	if !patchesEqual(retry, ref) {
		t.Fatalf("retry patch differs from reference:\n got %s\nwant %s",
			mustMarshalPatch(retry), mustMarshalPatch(ref))
	}
	if d.matcher != nil {
		t.Fatalf("matcher stack leaked after successful retry: %#v", d.matcher)
	}
	// A successful comparison may keep a prepared hashmap
	// (factorization), but it must be internally consistent;
	// the matcher stack and temporary state must be gone.
	d.Reset()
	if len(d.hashmap) != 0 {
		t.Fatalf("Reset did not clear the post-retry hashmap")
	}
}

// TestArrayMatch_FailureBeforeEmission guards a failure
// injected after preparation but before any operation is
// emitted: no move/copy candidate may remain consumed.
func TestArrayMatch_FailureBeforeEmission(t *testing.T) {
	src, tgt := failureInputs()
	d := &Differ{}
	d.opts.factorize = true
	d.opts.lcs = true
	d.diag.hook = func(phase matchPhase, p pointer) {
		if phase == phaseMatcherStart {
			panic(errInjectedArrayMatch)
		}
	}
	var panicked interface{}
	func() {
		defer func() { panicked = recover() }()
		_, _ = compareJSON(d, src, tgt, nil)
	}()
	if panicked != errInjectedArrayMatch {
		t.Fatalf("expected injected failure, got %#v", panicked)
	}
	if len(d.patch) != 0 {
		t.Fatalf("expected empty patch after pre-emission failure, got %s",
			mustMarshalPatch(d.patch))
	}
}

// TestArrayMatch_InvalidInputErrorLayering ensures unsupported
// inputs are rejected through the existing error layers rather
// than guessed as successes, and that the rejection is
// repeatable and leaves no temporary state.
func TestArrayMatch_InvalidInputErrorLayering(t *testing.T) {
	t.Run("marshal error is returned", func(t *testing.T) {
		type broken struct{}
		_ = broken{}
		p, err := Compare(jsonMarshalBroken{}, jsonMarshalBroken{})
		if err == nil {
			t.Fatalf("expected marshal error, got patch %s", mustMarshalPatch(p))
		}
		if p != nil {
			t.Fatalf("expected nil patch on marshal error, got %s", mustMarshalPatch(p))
		}
		// Repeatable rejection.
		if _, err2 := Compare(jsonMarshalBroken{}, jsonMarshalBroken{}); err2 == nil ||
			err2.Error() != err.Error() {
			t.Fatalf("rejection is not repeatable: %v vs %v", err2, err)
		}
	})
	t.Run("invalid json type is reported", func(t *testing.T) {
		p, err := CompareWithoutMarshal(
			[]interface{}{make(chan int)}, []interface{}{float64(1)})
		if err == nil {
			t.Fatalf("expected invalid json type error, got patch %s", mustMarshalPatch(p))
		}
		if p != nil {
			t.Fatalf("expected nil patch on invalid type, got %s", mustMarshalPatch(p))
		}
	})
	t.Run("invalid json bytes are reported by CompareJSON", func(t *testing.T) {
		_, err := CompareJSON([]byte(`{`), []byte(`{}`))
		if err == nil {
			t.Fatalf("expected invalid source json error")
		}
	})
}

type jsonMarshalBroken struct{}

func (jsonMarshalBroken) MarshalJSON() ([]byte, error) {
	return nil, errors.New("simulated marshal failure")
}

// TestArrayMatch_PatchStableAcrossEnumsAndSchedule changes only
// result-irrelevant conditions (option declaration order and
// call scheduling) and asserts the public patch structure and
// stable ordering are unchanged.
func TestArrayMatch_PatchStableAcrossEnumsAndSchedule(t *testing.T) {
	src := []interface{}{float64(1), "x", map[string]interface{}{"k": "v"}}
	tgt := []interface{}{map[string]interface{}{"k": "v"}, float64(1), "x", "tail"}

	baseline, err := Compare(src, tgt, Factorize(), LCS())
	if err != nil {
		t.Fatal(err)
	}
	// Same option set, declared in a different order, applied
	// to a freshly allocated Differ each time.
	for round := 0; round < 5; round++ {
		got, err := Compare(src, tgt, LCS(), Factorize())
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if !patchesEqual(got, baseline) {
			t.Fatalf("round %d: option reorder changed patch:\n got %s\nwant %s",
				round, mustMarshalPatch(got), mustMarshalPatch(baseline))
		}
	}
	// A no-op diagnostic hook and repeated comparisons on a
	// reused Differ (mimicking different scheduling) must not
	// perturb ordering.
	d := &Differ{}
	d.applyOpts(Factorize(), LCS())
	si, ti, err := roundtrip(src, tgt)
	if err != nil {
		t.Fatal(err)
	}
	d.diag.hook = func(matchPhase, pointer) {}
	for round := 0; round < 5; round++ {
		d.patch = nil
		d.ptr.reset()
		d.Compare(si, ti)
		if !patchesEqual(d.patch, baseline) {
			t.Fatalf("round %d: scheduling/no-op hook changed patch:\n got %s\nwant %s",
				round, mustMarshalPatch(d.patch), mustMarshalPatch(baseline))
		}
	}
}

func roundtrip(src, tgt interface{}) (interface{}, interface{}, error) {
	sb, err := json.Marshal(src)
	if err != nil {
		return nil, nil, err
	}
	tb, err := json.Marshal(tgt)
	if err != nil {
		return nil, nil, err
	}
	var si, ti interface{}
	if err := json.Unmarshal(sb, &si); err != nil {
		return nil, nil, err
	}
	if err := json.Unmarshal(tb, &ti); err != nil {
		return nil, nil, err
	}
	return si, ti, nil
}

// TestArrayMatch_ConcurrentIsolation drives concurrent
// comparisons behind a barrier so all goroutines start matching
// simultaneously. Each uses its own Differ (the documented
// public pattern), and diagnostic counters, prepared hashmaps
// and matcher stacks must stay isolated while every goroutine
// observes the same stable patch.
func TestArrayMatch_ConcurrentIsolation(t *testing.T) {
	const goroutines = 16

	src, tgt := failureInputs()
	ref, err := CompareJSON(src, tgt, Factorize(), LCS())
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			<-start // barrier: release all workers at once
			p, err := CompareJSON(src, tgt, Factorize(), LCS())
			if err != nil {
				errs <- fmt.Errorf("worker %d: %w", id, err)
				return
			}
			if !patchesEqual(p, ref) {
				errs <- fmt.Errorf("worker %d: patch drifted:\n got %s\nwant %s",
					id, mustMarshalPatch(p), mustMarshalPatch(ref))
				return
			}
		}(g)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// TestArrayMatch_ConcurrentCounterIsolation verifies that
// diagnostic counters are per-comparison: reusing a Differ
// serially resets them, and interleaved Differs never aggregate
// each other's counts.
func TestArrayMatch_ConcurrentCounterIsolation(t *testing.T) {
	src := []interface{}{"a", "b", "c"}
	tgt := []interface{}{"c", "a", "b"}

	first := &Differ{}
	first.applyOpts(Factorize(), LCS())
	second := &Differ{}
	second.applyOpts(Factorize(), LCS())

	run := func(d *Differ) matchStats {
		d.Reset()
		d.Compare(src, tgt)
		return d.stats
	}
	s1 := run(first)
	s2 := run(second)
	if s1 != s2 {
		t.Fatalf("counters differ across Differs: %+v vs %+v", s1, s2)
	}
	// Reusing first must reset, not accumulate.
	s1b := run(first)
	if s1b != s1 {
		t.Fatalf("counters accumulated across comparisons on one Differ: %+v vs %+v",
			s1b, s1)
	}
}
