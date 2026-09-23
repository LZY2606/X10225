package jsondiff

import (
	"encoding/json"
	"fmt"
	"testing"
)

func probe(t *testing.T, name string, src, tgt any, opts ...Option) {
	d := &Differ{}
	d.WithOpts(opts...)
	d.Compare(src, tgt)
	p, _ := json.MarshalIndent(d.Patch(), "", "  ")
	fmt.Printf("=== %s ===\n%s\n", name, p)
	if d.matchReport() != nil {
		for _, a := range d.matchReport().Arrays {
			fmt.Printf("  array %q stage=%s recovered=%t digests=%d hashProbes=%d deepEq=%d collisions=%d\n",
				a.Path, a.Stage, a.Recovered, a.Digests, a.HashProbes, a.DeepEqualCalls, a.HashCollisions)
		}
		r := d.matchReport()
		fmt.Printf("  moves: lookups=%d confirms=%d coll=%d prefixSkip=%d | copies: lookups=%d confirms=%d coll=%d\n",
			r.MoveLookups, r.MoveConfirms, r.MoveCollisions, r.MovePrefixSkip,
			r.CopyLookups, r.CopyConfirms, r.CopyCollisions)
	}
}

func TestProbe(t *testing.T) {
	lf := LCS()
	ff := Factorize()

	probe(t, "duplicates lcs+fac",
		[]any{"a", "b", "a", "c", "a"},
		[]any{"x", "a", "b", "a", "c"}, lf, ff)

	probe(t, "deep objects lcs+fac",
		[]any{map[string]any{"a": map[string]any{"x": 1.0}}, map[string]any{"b": 2.0}},
		[]any{map[string]any{"b": 2.0}, map[string]any{"a": map[string]any{"x": 1.0, "z": 3.0}}}, lf, ff)

	probe(t, "head/tail inserts lcs+fac",
		[]any{"b", "c"},
		[]any{"a", "b", "c", "d"}, lf, ff)

	probe(t, "cross moves lcs+fac",
		[]any{"a", "b"},
		[]any{"b", "a"}, lf, ff)

	probe(t, "cross moves no factorize",
		[]any{"a", "b"},
		[]any{"b", "a"}, lf)

	probe(t, "cross moves default (no lcs)",
		[]any{"a", "b"},
		[]any{"b", "a"})

	probe(t, "copy factorize lcs",
		map[string]any{"a": []any{"x", 1.0}, "b": map[string]any{"k": "v"}},
		map[string]any{"a": []any{"x", 1.0, 1.0}, "c": map[string]any{"k": "v"}}, lf, ff)
}
