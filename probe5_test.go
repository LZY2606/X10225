package jsondiff

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestProbe5(t *testing.T) {
	srcJSON := `["",[],{},false,null,""]`
	tgtJSON := `[{},"",[],null,false,""]`
	var src, tgt interface{}
	json.Unmarshal([]byte(srcJSON), &src)
	json.Unmarshal([]byte(tgtJSON), &tgt)
	s := src.([]interface{})
	g := tgt.([]interface{})
	d := Differ{}
	m := newMatcher(&d, s, g)
	for i := range s {
		for j := range g {
			if m.srcHash[i] == m.tgtHash[j] {
				fmt.Printf("hash-equal pair (%d,%d): src=%#v tgt=%#v eq=%v\n", i, j, s[i], g[j], m.equal(i, j))
			}
		}
	}
	fmt.Printf("confirms=%d\n", d.Stats().EqualityComparisons)
}
