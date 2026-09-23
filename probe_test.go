package jsondiff

import (
	"encoding/json"
	"fmt"
	"testing"
)

func probe(t *testing.T, name string, src, tgt interface{}, opts ...Option) {
	d := Differ{}
	d.applyOpts(opts...)
	d.Compare(src, tgt)
	b, _ := json.Marshal(d.Patch())
	fmt.Printf("=== %s\npatch=%s\nstats=%+v\n", name, b, d.Stats())
}

func TestProbe(t *testing.T) {
	arr := func(v ...interface{}) []interface{} { return v }
	obj := func() map[string]interface{} {
		return map[string]interface{}{"a": map[string]interface{}{"b": arr(1.0, 2.0, map[string]interface{}{"c": 3.0})}}
	}
	probe(t, "duplicates", arr("x", "x", "y", "x"), arr("x", "y", "x", "x"), LCS(), Factorize())
	probe(t, "deep-objects", arr(obj(), "z"), arr("z", obj()), LCS(), Factorize())
	probe(t, "collision", arr("", arr(), map[string]interface{}{}, false, nil, ""), arr(map[string]interface{}{}, "", arr(), nil, false, ""), LCS(), Factorize())
	probe(t, "head-tail", arr(2.0, 3.0), arr(1.0, 2.0, 3.0, 4.0), LCS())
	probe(t, "cross-move", arr("a", "b", "c"), arr("c", "a", "b"), LCS(), Factorize())
	probe(t, "cross-move-nofactor", arr("a", "b", "c"), arr("c", "a", "b"), LCS())
	probe(t, "cross-move-invertible", arr("a", "b", "c"), arr("c", "a", "b"), LCS(), Factorize(), Invertible())
	probe(t, "cross-move-noopts", arr("a", "b", "c"), arr("c", "a", "b"))
	probe(t, "threshold", arr("a", "b", "c"), arr("c", "a", "b"), LCS(), Factorize())
}
