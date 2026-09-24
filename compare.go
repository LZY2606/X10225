package jsondiff

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Compare compares the JSON representations of the
// given values and returns the differences relative
// to the former as a list of JSON Patch operations.
func Compare(source, target interface{}, opts ...Option) (Patch, error) {
	var d Differ
	d.applyOpts(opts...)

	return compare(&d, source, target)
}

// CompareJSON compares the given JSON documents and
// returns the differences relative to the former as
// a list of JSON Patch operations.
func CompareJSON(source, target []byte, opts ...Option) (Patch, error) {
	var d Differ
	d.applyOpts(opts...)

	return compareJSON(&d, source, target, d.opts.unmarshal)
}

// CompareWithoutMarshal is similar to Compare, but it assumes
// that the given interface values consists only of primitives
// Go types that are recognized by the json.Unmarshal function,
// and therefore does not marshal/unmarshal before comparison.
func CompareWithoutMarshal(source, target interface{}, opts ...Option) (patch Patch, err error) {
	var d Differ

	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(error); ok && errors.Is(e, errComparisonInterrupted) {
				patch, err = nil, fmt.Errorf("%w", errComparisonInterrupted)
				return
			}
			e := r.(invalidJSONTypeError)
			err = fmt.Errorf("jsondiff: invalid json type: %T", e.t)
			patch = nil
		}
	}()
	d.applyOpts(opts...)
	d.Compare(source, target)
	patch = d.patch

	return patch, err
}

func compare(d *Differ, src, tgt interface{}) (patch Patch, err error) {
	defer func() {
		if r := recover(); r != nil {
			if errors.Is(unwrapSentinel(r), errComparisonInterrupted) {
				patch, err = nil, fmt.Errorf("%w", errComparisonInterrupted)
				return
			}
			panic(r)
		}
	}()
	if d.opts.marshal == nil {
		d.opts.marshal = json.Marshal
	}
	if d.opts.unmarshal == nil {
		d.opts.unmarshal = json.Unmarshal
	}
	si, _, err := marshalUnmarshal(src, d.opts)
	if err != nil {
		return nil, err
	}
	ti, tb, err := marshalUnmarshal(tgt, d.opts)
	if err != nil {
		return nil, err
	}
	d.targetBytes = tb

	d.Compare(si, ti)
	return d.patch, nil
}

func unwrapSentinel(r interface{}) error {
	if e, ok := r.(error); ok {
		return e
	}
	return nil
}

func compareJSON(d *Differ, src, tgt []byte, unmarshal unmarshalFunc) (patch Patch, err error) {
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(error); ok && errors.Is(e, errComparisonInterrupted) {
				patch, err = nil, fmt.Errorf("%w", errComparisonInterrupted)
				return
			}
			panic(r)
		}
	}()
	if unmarshal == nil {
		unmarshal = json.Unmarshal
	}
	var si, ti interface{}
	if err := unmarshal(src, &si); err != nil {
		return nil, err
	}
	if err := unmarshal(tgt, &ti); err != nil {
		return nil, err
	}
	d.targetBytes = tgt

	d.Compare(si, ti)
	return d.patch, nil
}

// marshalUnmarshal returns the result of unmarshaling
// the JSON representation of the given interface value.
func marshalUnmarshal(v any, opts options) (interface{}, []byte, error) {
	b, err := opts.marshal(v)
	if err != nil {
		return nil, nil, err
	}
	var i interface{}
	if err := opts.unmarshal(b, &i); err != nil {
		return nil, nil, err
	}
	return i, b, nil
}
