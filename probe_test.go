package jsondiff

import (
	"encoding/json"
	"fmt"
	"testing"
)

func unm(s string) interface{} {
	var v interface{}
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		panic(err)
	}
	return v
}

func TestProbe(t *testing.T) {
	run := func(name, src, tgt string, opts ...Option) {
		d := &Differ{targetBytes: []byte(tgt)}
		d.WithOpts(opts...)
		d.Compare(unm(src), unm(tgt))
		b, _ := json.Marshal(d.Patch())
		fmt.Printf("=== %s\n%s\nstats=%+v\n", name, b, d.Stats())
	}
	run("headtail", `[1,2,3]`, `[0,1,2,3,4]`, LCS())
	run("crossmove", `{"a":[1,2],"b":[]}`, `{"a":[1],"b":[2]}`, Factorize())
	run("crossmove-lcs", `{"a":[1,2],"b":[]}`, `{"a":[1],"b":[2]}`, Factorize(), LCS())
	run("dups", `["a","b","a","c"]`, `["b","a","a","c"]`, LCS())
	run("dups2", `["x","x","y"]`, `["x","y","y"]`, LCS())
	run("crossmove2", `{"a":["x"],"b":["y"]}`, `{"a":[],"b":["y","x"]}`, Factorize())
	run("disabled", `{"a":[1,2],"b":[]}`, `{"a":[1],"b":[2]}`)
	run("invertible", `{"a":[1,2],"b":[]}`, `{"a":[1],"b":[2]}`, Factorize(), Invertible())
	run("copy", `{"a":[1,2],"b":[]}`, `{"a":[1,2],"b":[2]}`, Factorize())
	// json.Number collision
	src := []interface{}{json.Number("1"), json.Number("2"), json.Number("1")}
	tgt := []interface{}{json.Number("2"), json.Number("1"), json.Number("1")}
	d := &Differ{}
	d.WithOpts(LCS())
	d.Compare(src, tgt)
	b, _ := json.Marshal(d.Patch())
	fmt.Printf("=== collision\n%s\nstats=%+v\n", b, d.Stats())
	// deep objects
	run("deep", `{"a":{"b":{"c":[1,2,{"d":3}]}}}`, `{"a":{"b":{"c":[1,2,{"d":4}]}}}`, LCS(), Factorize())
	// comparison count probe: distinct elements
	n := 32
	s1, s2 := make([]interface{}, n), make([]interface{}, n)
	for i := range s1 {
		s1[i] = float64(i)
		s2[i] = float64(i + n)
	}
	d2 := &Differ{}
	d2.WithOpts(LCS())
	d2.Compare(s1, s2)
	fmt.Printf("=== distinct32 stats=%+v (old fill would be %d)\n", d2.Stats(), n*n)
	// duplicates alphabet
	alph := []string{"a", "b", "c"}
	s3, s4 := make([]interface{}, 30), make([]interface{}, 30)
	for i := range s3 {
		s3[i] = alph[i%3]
		s4[i] = alph[(i+1)%3]
	}
	d3 := &Differ{}
	d3.WithOpts(LCS())
	d3.Compare(s3, s4)
	fmt.Printf("=== dupalpha30 stats=%+v (old fill would be 900)\n", d3.Stats())
}
