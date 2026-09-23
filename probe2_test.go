package jsondiff

import (
	"encoding/json"
	"fmt"
	"testing"
)

func probe2(t *testing.T, name string, srcJSON, tgtJSON string, opts ...Option) {
	var src, tgt interface{}
	json.Unmarshal([]byte(srcJSON), &src)
	json.Unmarshal([]byte(tgtJSON), &tgt)
	d := Differ{}
	d.applyOpts(opts...)
	d.Compare(src, tgt)
	pb, _ := json.Marshal(d.Patch())
	out, err := d.Patch().apply([]byte(srcJSON), true)
	ok := err == nil && jsonEqual(out, []byte(tgtJSON))
	fmt.Printf("=== %s ok=%v err=%v\npatch=%s\nstats=%+v\n", name, ok, err, pb, d.Stats())
}

func jsonEqual(a, b []byte) bool {
	var x, y interface{}
	json.Unmarshal(a, &x)
	json.Unmarshal(b, &y)
	return deepEqual(x, y)
}

func TestProbe2(t *testing.T) {
	probe2(t, "duplicates", `["x","x","y","x"]`, `["x","y","x","x"]`, LCS(), Factorize())
	probe2(t, "deep-objects", `[{"a":{"b":[1,2,{"c":3}]}},"z"]`, `["z",{"a":{"b":[1,2,{"c":3}]}}]`, LCS(), Factorize())
	probe2(t, "collision", `["",[],{},false,null,""]`, `[{},"",[],null,false,""]`, LCS(), Factorize())
	probe2(t, "head-tail", `[2,3]`, `[1,2,3,4]`, LCS())
	probe2(t, "cross-move", `["a","b","c"]`, `["c","a","b"]`, LCS(), Factorize())
	probe2(t, "cross-move-nofactor", `["a","b","c"]`, `["c","a","b"]`, LCS())
	probe2(t, "cross-move-invertible", `["a","b","c"]`, `["c","a","b"]`, LCS(), Factorize(), Invertible())
	probe2(t, "cross-move-noopts", `["a","b","c"]`, `["c","a","b"]`)
}
