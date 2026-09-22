package jsondiff

import (
	"encoding/json"
	"fmt"
	"testing"
)

func applyops(src []byte, ops []Operation) []byte {
	cur := src
	for _, op := range ops {
		out, err := Patch{op}.apply(cur, false)
		fmt.Printf("  %s -> %s (err %v)\n", op, string(out), err)
		cur = out
	}
	return cur
}

func TestDbgCheck(t *testing.T) {
	src := []byte(`[true,0,false,0,2,false,true,false]`)
	ops := []Operation{
		{Type:"move", From:"/0", Path:"/6"},
		{Type:"remove", Path:"/0"},
		{Type:"remove", Path:"/0"},
		{Type:"replace", Path:"/0"},
		{Type:"remove", Path:"/1"},
		{Type:"replace", Path:"/2"},
		{Type:"add", Path:"/4"},
		{Type:"add", Path:"/5"},
		{Type:"add", Path:"/7"},
	}
	_ = json.Marshal
	applyops(src, ops)
}
