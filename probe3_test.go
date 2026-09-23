package jsondiff

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestProbe3(t *testing.T) {
	srcJSON := `["",[],{},false,null,""]`
	tgtJSON := `[{},"",[],null,false,""]`
	var src, tgt interface{}
	json.Unmarshal([]byte(srcJSON), &src)
	json.Unmarshal([]byte(tgtJSON), &tgt)
	d := Differ{}
	d.applyOpts(LCS(), Factorize())
	d.Compare(src, tgt)
	for i, op := range d.Patch() {
		fmt.Printf("op%d: %s\n", i, op.String())
	}
	out, err := d.Patch().apply([]byte(srcJSON), true)
	fmt.Printf("out=%s err=%v\n", out, err)
}
