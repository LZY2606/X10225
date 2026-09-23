package jsondiff

import (
	"fmt"
	"testing"
)

func TestProbe6(t *testing.T) {
	for _, n := range []int{8, 64} {
		src := make([]interface{}, 0, n)
		tgt := make([]interface{}, 0, n)
		half := n / 2
		for i := 0; i < half; i++ {
			src = append(src, fmt.Sprintf("a%03d", i))
		}
		for i := 0; i < half; i++ {
			src = append(src, fmt.Sprintf("b%03d", i))
		}
		tgt = append(tgt, src[half:]...)
		tgt = append(tgt, src[:half]...)
		d := Differ{}
		d.applyOpts(LCS(), Factorize())
		d.Compare(src, tgt)
		st := d.Stats()
		fmt.Printf("n=%d ops=%d EQ=%d HC=%d oldLCSFill=%d\n", n, len(d.Patch()), st.EqualityComparisons, st.HashComputations, n*n)
	}
}
