package jsondiff

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestProbeCompat(t *testing.T) {
	arr := func(v ...interface{}) []interface{} { return v }
	deep := func() map[string]interface{} {
		return map[string]interface{}{"a": map[string]interface{}{"b": arr(1.0, 2.0, map[string]interface{}{"c": 3.0})}}
	}
	cases := []struct {
		name string
		src  interface{}
		tgt  interface{}
		opts []Option
	}{
		{"duplicates", arr("x", "x", "y", "x"), arr("x", "y", "x", "x"), []Option{LCS(), Factorize()}},
		{"deep-objects", arr(deep(), "z"), arr("z", deep()), []Option{LCS(), Factorize()}},
		{"collision-lcs", arr("", arr(), map[string]interface{}{}, false, nil, ""), arr(map[string]interface{}{}, "", arr(), nil, false, ""), []Option{LCS()}},
		{"collision-factor", arr("", arr(), map[string]interface{}{}, false, nil, ""), arr(map[string]interface{}{}, "", arr(), nil, false, ""), []Option{LCS(), Factorize()}},
		{"collision-invert", arr("", arr(), map[string]interface{}{}, false, nil, ""), arr(map[string]interface{}{}, "", arr(), nil, false, ""), []Option{LCS(), Factorize(), Invertible()}},
		{"head-tail", arr(2.0, 3.0), arr(1.0, 2.0, 3.0, 4.0), []Option{LCS()}},
		{"cross-move", arr("a", "b", "c"), arr("c", "a", "b"), []Option{LCS(), Factorize()}},
		{"cross-move-nofactor", arr("a", "b", "c"), arr("c", "a", "b"), []Option{LCS()}},
		{"cross-move-invertible", arr("a", "b", "c"), arr("c", "a", "b"), []Option{LCS(), Factorize(), Invertible()}},
		{"cross-move-noopts", arr("a", "b", "c"), arr("c", "a", "b"), nil},
		{"equiv", arr(1.0, 2.0, 3.0), arr(3.0, 2.0, 1.0), []Option{Equivalent()}},
		{"rationalize", arr("a", "b", "c"), arr("c", "a", "b"), []Option{LCS(), Rationalize()}},
		{"all-opts", arr(deep(), "x", deep()), arr("x", deep(), deep()), []Option{LCS(), Factorize(), Rationalize(), Equivalent()}},
	}
	for _, tc := range cases {
		d := Differ{}
		d.applyOpts(tc.opts...)
		d.Compare(tc.src, tc.tgt)
		b, _ := json.Marshal(d.Patch())
		fmt.Printf("### %s %s\n", tc.name, b)
	}
}
