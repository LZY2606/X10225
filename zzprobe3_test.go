package jsondiff

import (
	"fmt"
	"testing"
)

type countingHash struct {
	seed uint64
	n    int
}

func TestProbe3(t *testing.T) {
	// deep object reorder -> factorize should produce a move
	src := []any{
		map[string]any{"a": map[string]any{"x": 1.0, "y": []any{1.0, 2.0}}},
		map[string]any{"b": 2.0},
	}
	tgt := []any{
		map[string]any{"b": 2.0},
		map[string]any{"a": map[string]any{"x": 1.0, "y": []any{1.0, 2.0}}},
	}
	for _, useLCS := range []bool{true, false} {
		d := &Differ{}
		opts := []Option{Factorize()}
		if useLCS {
			opts = append(opts, LCS())
		}
		d.WithOpts(opts...)
		d.Compare(src, tgt)
		fmt.Printf("deep-reorder lcs=%v patch: %v\n", useLCS, d.Patch())
	}

	// multiset dup multiplicity, LCS+equivalent
	s1 := []any{"x", "x", "y"}
	g1 := []any{"x", "y", "y"}
	d := &Differ{}
	d.WithOpts(LCS(), Equivalent())
	d.Compare(s1, g1)
	fmt.Printf("multiset dup-mismatch len=%d patch: %v\n", len(d.Patch()), d.Patch())

	s2 := []any{"x", "x", "y"}
	g2 := []any{"y", "x", "x"}
	d2 := &Differ{}
	d2.WithOpts(LCS(), Equivalent())
	d2.Compare(s2, g2)
	fmt.Printf("multiset dup-match len=%d stats: %+v\n", len(d2.Patch()), d2.matchReport().Arrays)

	// multiset failure recovery at root
	fired := 0
	d3 := &Differ{}
	d3.failHook = func(stage matchStage, path string) bool {
		if stage == stageMultiset && path == "" && fired == 0 {
			fired++
			return true
		}
		return false
	}
	d3.WithOpts(LCS(), Equivalent())
	d3.Compare(s2, g2)
	fmt.Printf("multiset fail-recover len=%d stats: %+v\n", len(d3.Patch()), d3.matchReport().Arrays)

	// nested arrays diagnostic order
	doc1 := map[string]any{"a": []any{"m", "n"}, "b": []any{"p", "q"}, "c": map[string]any{"d": []any{"r", "s"}}}
	doc2 := map[string]any{"a": []any{"n", "m"}, "b": []any{"q", "p"}, "c": map[string]any{"d": []any{"s", "r"}}}
	d4 := &Differ{}
	d4.WithOpts(LCS(), Factorize())
	d4.Compare(doc1, doc2)
	fmt.Printf("nested patches: %v\n", d4.Patch())
	for _, a := range d4.matchReport().Arrays {
		fmt.Printf("  array %q deepEq=%d\n", a.Path, a.DeepEqualCalls)
	}

	// counting hash: digest per element exactly once
	h := &countingHash{}
	d5 := &Differ{}
	d5.customHash = h.hash
	d5.WithOpts(LCS())
	d5.Compare([]any{"a", "b", "c"}, []any{"c", "b", "d"})
	fmt.Printf("digest calls=%d (want 6)\n", h.n)
}

func (h *countingHash) hash(v any, sort bool) uint64 {
	h.n++
	hasher := hasher{}
	return hasher.digest(v, sort)
}
