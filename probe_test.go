package jsondiff
import ("encoding/json";"fmt";"testing")
func TestProbeN(t *testing.T) {
	var si, ti interface{}
	json.Unmarshal([]byte(`[{"tags":["n","n",1]},{"tags":["n","n",2]}]`), &si)
	json.Unmarshal([]byte(`[{"tags":["n","n",1]},{"tags":["n","n",1]}]`), &ti)
	src := si.([]interface{}); tgt := ti.([]interface{})
	p := Patch{{Type: OperationReplace, Path: "/1/tags/2", Value: 1.0}}
	fmt.Println("validator:", arrayRegionValid("", src, tgt, p))
	// What is the actual factored region from the real run?
	d := &Differ{}; d.applyOpts(LCS(), Factorize())
	defer func(){ recover() }()
	d.Compare(si, ti)
	b,_ := json.Marshal(d.Patch())
	fmt.Printf("real patch=%s\n", b)
}
