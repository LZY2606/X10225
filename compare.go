package jsondiff

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ErrArrayMatchingInterrupted is the public error reported
// when an array comparison fails mid-run through the
// package-internal diagnostic failure injection. The
// comparison is rolled back atomically, and retrying with
// the same input yields the stable, expected result.
var ErrArrayMatchingInterrupted = errors.New("jsondiff: array matching was interrupted")

// errMatcherPanic translates an internal, recoverable
// matching panic into an error belonging to the package's
// error model, leaving other panics propagate unchanged.
func errMatcherPanic(r any) error {
	if f, ok := r.(matcherFault); ok {
		return fmt.Errorf("%w (at pair %d)", ErrArrayMatchingInterrupted, f.at)
	}
	if e, ok := r.(invalidJSONTypeError); ok {
		return fmt.Errorf("jsondiff: invalid json type: %T", e.t)
	}
	panic(r)
}

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
			err = errMatcherPanic(r)
			patch = nil
		}
	}()
	d.applyOpts(opts...)
	d.Compare(source, target)
	patch = d.patch

	return patch, err
}

func compare(d *Differ, src, tgt interface{}) (Patch, error) {
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

func compareJSON(d *Differ, src, tgt []byte, unmarshal unmarshalFunc) (patch Patch, err error) {
	if unmarshal == nil {
		unmarshal = json.Unmarshal
	}
	var si, ti interface{}
	if err = unmarshal(src, &si); err != nil {
		return nil, err
	}
	if err = unmarshal(tgt, &ti); err != nil {
		return nil, err
	}
	d.targetBytes = tgt

	defer func() {
		if r := recover(); r != nil {
			err = errMatcherPanic(r)
			patch = nil
		}
	}()
	d.Compare(si, ti)
	return d.patch, nil
}
