package jsondiff

import (
	"encoding/json"
	"fmt"
	"testing"
)

func strictOrig(src []interface{}, p Patch) ([]interface{}, bool) {
	cur := append([]interface{}(nil), src...)
	ri := func(path string) int {
		var n int
		for i := 1; i < len(path); i++ { n = n*10 + int(path[i]-'0') }
		return n
	}
	for _, op := range p {
		idx := ri(op.Path)
		switch op.Type {
		case OperationRemove:
			if idx >= len(cur) { return nil, false }
			cur = append(cur[:idx], cur[idx+1:]...)
		case OperationAdd:
			cur = append(cur, nil)
			copy(cur[idx+1:], cur[idx:])
			cur[idx] = op.Value
		case OperationReplace:
			cur[idx] = op.Value
		case OperationMove:
			f := ri(op.From)
			v := cur[f]
			cur = append(cur[:f], cur[f+1:]...)
			cur = append(cur, nil)
			copy(cur[idx+1:], cur[idx:])
			cur[idx] = v
		}
	}
	return cur, true
}

func TestReproOrig2(t *testing.T) {
	for _, c := range []struct{ s, g []interface{} }{
		{[]interface{}{3.,1,0,2}, []interface{}{1.,2,3,0}},
		{[]interface{}{2.,3,0,1}, []interface{}{0.,2,1,3,9}},
	} {
		var si, ti interface{}
		sb, _ := json.Marshal(c.s); tb, _ := json.Marshal(c.g)
		json.Unmarshal(sb, &si); json.Unmarshal(tb, &ti)
		d := &Differ{}; d.applyOpts(LCS(), Factorize()); d.Compare(si, ti)
		out, ok := strictOrig(c.s, d.Patch())
		b, _ := json.Marshal(d.Patch())
		fmt.Printf("ORIG %v->%v out=%v ok=%v patch=%s\n", c.s, c.g, out, ok, b)
	}
}
