package jsondiff

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestProbe4(t *testing.T) {
	srcJSON := `["",[],{},false,null,""]`
	tgtJSON := `[{},"",[],null,false,""]`
	for _, tc := range []struct {
		name string
		opts []Option
	}{
		{"lcs", []Option{LCS()}},
		{"lcs+factor+invert", []Option{LCS(), Factorize(), Invertible()}},
		{"lcs+factor", []Option{LCS(), Factorize()}},
	} {
		var src, tgt interface{}
		json.Unmarshal([]byte(srcJSON), &src)
		json.Unmarshal([]byte(tgtJSON), &tgt)
		d := Differ{}
		d.applyOpts(tc.opts...)
		d.Compare(src, tgt)
		b, _ := json.Marshal(d.Patch())
		fmt.Printf("=== %s\npatch=%s\nstats=%+v\n", tc.name, b, d.Stats())
	}
}
