// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build go1.5

package typeutil_test

// TODO(adonovan):
// - test use of explicit hasher across two maps.
// - test hashcodes are consistent with equals for a range of types
//   (e.g. all types generated by type-checking some body of real code).

import (
	"go/types"
	"testing"

	"golang.org/x/tools/go/types/typeutil"
)

var (
	tStr      = types.Typ[types.String]             // string
	tPStr1    = types.NewPointer(tStr)              // *string
	tPStr2    = types.NewPointer(tStr)              // *string, again
	tInt      = types.Typ[types.Int]                // int
	tChanInt1 = types.NewChan(types.RecvOnly, tInt) // <-chan int
	tChanInt2 = types.NewChan(types.RecvOnly, tInt) // <-chan int, again
)

func checkEqualButNotIdentical(t *testing.T, x, y types.Type, comment string) {
	if !types.Identical(x, y) {
		t.Errorf("%s: not equal: %s, %s", comment, x, y)
	}
	if x == y {
		t.Errorf("%s: identical: %v, %v", comment, x, y)
	}
}

func TestAxioms(t *testing.T) {
	checkEqualButNotIdentical(t, tPStr1, tPStr2, "tPstr{1,2}")
	checkEqualButNotIdentical(t, tChanInt1, tChanInt2, "tChanInt{1,2}")
}

func TestMap(t *testing.T) {
	var tmap *typeutil.Map

	// All methods but Set are safe on on (*T)(nil).
	tmap.Len()
	tmap.At(tPStr1)
	tmap.Delete(tPStr1)
	tmap.KeysString()
	tmap.String()

	tmap = new(typeutil.Map)

	// Length of empty map.
	if l := tmap.Len(); l != 0 {
		t.Errorf("Len() on empty Map: got %d, want 0", l)
	}
	// At of missing key.
	if v := tmap.At(tPStr1); v != nil {
		t.Errorf("At() on empty Map: got %v, want nil", v)
	}
	// Deletion of missing key.
	if tmap.Delete(tPStr1) {
		t.Errorf("Delete() on empty Map: got true, want false")
	}
	// Set of new key.
	if prev := tmap.Set(tPStr1, "*string"); prev != nil {
		t.Errorf("Set() on empty Map returned non-nil previous value %s", prev)
	}

	// Now: {*string: "*string"}

	// Length of non-empty map.
	if l := tmap.Len(); l != 1 {
		t.Errorf("Len(): got %d, want 1", l)
	}
	// At via insertion key.
	if v := tmap.At(tPStr1); v != "*string" {
		t.Errorf("At(): got %q, want \"*string\"", v)
	}
	// At via equal key.
	if v := tmap.At(tPStr2); v != "*string" {
		t.Errorf("At(): got %q, want \"*string\"", v)
	}
	// Iteration over sole entry.
	tmap.Iterate(func(key types.Type, value interface{}) {
		if key != tPStr1 {
			t.Errorf("Iterate: key: got %s, want %s", key, tPStr1)
		}
		if want := "*string"; value != want {
			t.Errorf("Iterate: value: got %s, want %s", value, want)
		}
	})

	// Setion with key equal to present one.
	if prev := tmap.Set(tPStr2, "*string again"); prev != "*string" {
		t.Errorf("Set() previous value: got %s, want \"*string\"", prev)
	}

	// Setion of another association.
	if prev := tmap.Set(tChanInt1, "<-chan int"); prev != nil {
		t.Errorf("Set() previous value: got %s, want nil", prev)
	}

	// Now: {*string: "*string again", <-chan int: "<-chan int"}

	want1 := "{*string: \"*string again\", <-chan int: \"<-chan int\"}"
	want2 := "{<-chan int: \"<-chan int\", *string: \"*string again\"}"
	if s := tmap.String(); s != want1 && s != want2 {
		t.Errorf("String(): got %s, want %s", s, want1)
	}

	want1 = "{*string, <-chan int}"
	want2 = "{<-chan int, *string}"
	if s := tmap.KeysString(); s != want1 && s != want2 {
		t.Errorf("KeysString(): got %s, want %s", s, want1)
	}

	// Keys().
	I := types.Identical
	switch k := tmap.Keys(); {
	case I(k[0], tChanInt1) && I(k[1], tPStr1): // ok
	case I(k[1], tChanInt1) && I(k[0], tPStr1): // ok
	default:
		t.Errorf("Keys(): got %v, want %s", k, want2)
	}

	if l := tmap.Len(); l != 2 {
		t.Errorf("Len(): got %d, want 1", l)
	}
	// At via original key.
	if v := tmap.At(tPStr1); v != "*string again" {
		t.Errorf("At(): got %q, want \"*string again\"", v)
	}
	hamming := 1
	tmap.Iterate(func(key types.Type, value interface{}) {
		switch {
		case I(key, tChanInt1):
			hamming *= 2 // ok
		case I(key, tPStr1):
			hamming *= 3 // ok
		}
	})
	if hamming != 6 {
		t.Errorf("Iterate: hamming: got %d, want %d", hamming, 6)
	}

	if v := tmap.At(tChanInt2); v != "<-chan int" {
		t.Errorf("At(): got %q, want \"<-chan int\"", v)
	}
	// Deletion with key equal to present one.
	if !tmap.Delete(tChanInt2) {
		t.Errorf("Delete() of existing key: got false, want true")
	}

	// Now: {*string: "*string again"}

	if l := tmap.Len(); l != 1 {
		t.Errorf("Len(): got %d, want 1", l)
	}
	// Deletion again.
	if !tmap.Delete(tPStr2) {
		t.Errorf("Delete() of existing key: got false, want true")
	}

	// Now: {}

	if l := tmap.Len(); l != 0 {
		t.Errorf("Len(): got %d, want %d", l, 0)
	}
	if s := tmap.String(); s != "{}" {
		t.Errorf("Len(): got %q, want %q", s, "")
	}
}
