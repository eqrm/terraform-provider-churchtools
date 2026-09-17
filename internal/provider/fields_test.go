package provider

import (
	"errors"
	"testing"
)

// CT answering a create with no usable id is not recoverable: idString gives
// "", Terraform accepts "" as a known computed string, and the NEXT read then
// addresses the collection endpoint and decodes an array into a Row. The
// resource is stuck from then on. Fail the apply instead.
func TestRequireID(t *testing.T) {
	for _, tc := range []struct {
		name    string
		row     map[string]any
		want    string
		wantErr bool
	}{
		{"numeric id", map[string]any{"id": float64(7)}, "7", false},
		{"zero id is valid", map[string]any{"id": float64(0)}, "0", false},
		{"string id", map[string]any{"id": "42"}, "42", false},
		{"missing id", map[string]any{"name": "Neustadt"}, "", true},
		{"null id", map[string]any{"id": nil}, "", true},
		{"empty string id", map[string]any{"id": ""}, "", true},
		{"nil row", nil, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := requireID(tc.row)
			if gotErr := err != nil; gotErr != tc.wantErr {
				t.Fatalf("requireID(%v) err = %v, wantErr %v", tc.row, err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Errorf("requireID(%v) = %q, want %q", tc.row, got, tc.want)
			}
			if tc.wantErr && !errors.Is(err, errNoID) {
				t.Errorf("err = %v, want errNoID", err)
			}
		})
	}
}

// Parity with ct-cli's truncatePadded (registry.ts): pad by repeating the value,
// or "x" when it is empty, then truncate. Padding with '.' would leave a stray
// dot in the ChurchTools UI on every short group-type name.
func TestTruncatePadded(t *testing.T) {
	for _, tc := range []struct {
		name     string
		in       string
		max, pad int
		want     string
	}{
		{"single char pads by repeat", "A", 32, 2, "AA"},
		{"empty pads with x", "", 32, 2, "xx"},
		{"empty pads with x to one", "", 32, 1, "x"},
		{"long enough is untouched", "Kleingruppe", 32, 2, "Kleingruppe"},
		{"truncates past max", "Kleingruppe", 5, 2, "Klein"},
		{"repeat then truncate", "AB", 3, 4, "ABA"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := truncatePadded(tc.in, tc.max, tc.pad); got != tc.want {
				t.Errorf("truncatePadded(%q, %d, %d) = %q, want %q", tc.in, tc.max, tc.pad, got, tc.want)
			}
		})
	}
}
