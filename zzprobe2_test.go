package jsondiff

import (
	"errors"
	"fmt"
	"testing"
)

func TestProbe2(t *testing.T) {
	src := []any{"a", "b"}
	tgt := []any{"b", "a"}

	// Constant hash: every pair collides.
	{
		d := &Differ{}
		d.customHash = func(v any, sort bool) uint64 { return 1 }
		d.WithOpts(LCS(), Factorize())
		d.Compare(src, tgt)
		fmt.Printf("constant-hash patch: %v\n", d.Patch())
		for _, a := range d.matchReport().Arrays {
			fmt.Printf("  stats: deepEq=%d coll=%d\n", a.DeepEqualCalls, a.HashCollisions)
		}
		r := d.matchReport()
		fmt.Printf("  moveConfirms=%d moveColl=%d\n", r.MoveConfirms, r.MoveCollisions)
	}
	// Failure injection at LCS stage, fire once.
	{
		d := &Differ{}
		fired := 0
		d.failHook = func(stage matchStage, path string) bool {
			fired++
			if fired == 1 {
				return true
			}
			return false
		}
		d.WithOpts(LCS())
		d.Compare(src, tgt)
		fmt.Printf("fail-LCS patch: %v fired=%d\n", d.Patch(), fired)
		for _, a := range d.matchReport().Arrays {
			fmt.Printf("  stats: recovered=%t deepEq=%d\n", a.Recovered, a.DeepEqualCalls)
		}
	}
	// Failure at digest stage.
	{
		d := &Differ{}
		fired := 0
		d.failHook = func(stage matchStage, path string) bool {
			if stage == stageDigest && fired == 0 {
				fired++
				return true
			}
			return false
		}
		d.WithOpts(LCS())
		d.Compare(src, tgt)
		fmt.Printf("fail-digest patch: %v fired=%d\n", d.Patch(), fired)
		for _, a := range d.matchReport().Arrays {
			fmt.Printf("  stats: recovered=%t deepEq=%d\n", a.Recovered, a.DeepEqualCalls)
		}
	}
	// Multiset failure with equivalent option.
	{
		s := []any{"a", "e", "c", "b", "d"}
		g := []any{"a", "b", "c", "d", "e"}
		d := &Differ{}
		fired := 0
		d.failHook = func(stage matchStage, path string) bool {
			if stage == stageMultiset && fired == 0 {
				fired++
				return true
			}
			return false
		}
		d.WithOpts(LCS(), Equivalent())
		d.Compare(s, g)
		fmt.Printf("fail-multiset patch(len=%d): %v fired=%d\n", len(d.Patch()), d.Patch(), fired)
		for _, a := range d.matchReport().Arrays {
			fmt.Printf("  stats: stage=%s recovered=%t\n", a.Stage, a.Recovered)
		}
	}
	// sentinel identity
	fmt.Println("sentinel:", errors.Is(errArrayMatchAborted, errArrayMatchAborted))
}
