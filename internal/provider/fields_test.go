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
