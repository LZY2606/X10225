package jsondiff

import (
	"encoding/json"
	"fmt"
	"testing"
)

func runCase(src, tgt string, opts ...Option) string {
	var s, t2 interface{}
	json.Unmarshal([]byte(src), &s)
	json.Unmarshal([]byte(tgt), &t2)
	d := Differ{}
	d.applyOpts(opts...)
	d.Compare(s, t2)
	b, _ := json.Marshal(d.Patch())
	return string(b)
}

func TestScratchStability(t *testing.T) {
	seen := map[string]int{}
	for i := 0; i < 300; i++ {
		seen[runCase(`{"a":[1,2],"b":[1,2]}`, `{"a":[1,2],"b":[1,2],"c":[1,2]}`, LCS(), Factorize())]++
	}
	fmt.Println(seen)
	seen2 := map[string]int{}
	for i := 0; i < 300; i++ {
		seen2[runCase(`{"a":{"x":[1,2]},"b":{"x":[1,2]}}`, `{"a":{"x":[1,2]},"b":{"x":[1,2]},"c":[1,2]}`, LCS(), Factorize())]++
	}
	fmt.Println(seen2)
}
