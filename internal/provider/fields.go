package provider

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// idString renders a JSON-decoded CT id as a string. CT ids arrive as float64
// from encoding/json; id 0 is valid (the Mainz campus), so this never treats
// 0 as absent.
func idString(v any) string {
	switch n := v.(type) {
	case float64:
		return strconv.FormatInt(int64(n), 10)
	case string:
		return n
	default:
		return ""
	}
}

func stringField(row map[string]any, key string) string {
	if s, ok := row[key].(string); ok {
		return s
	}
	return ""
}

func intField(row map[string]any, key string) int64 {
	if n, ok := row[key].(float64); ok {
		return int64(n)
	}
	return 0
}

func boolField(row map[string]any, key string) bool {
	b, _ := row[key].(bool)
	return b
}

// errNoID marks a create/update response that carried no usable id.
var errNoID = errors.New("ChurchTools hat keine id zurueckgegeben")

// requireID pulls the id out of a create/update response. An absent id must be
// an error, never "": Terraform accepts "" as a known computed string, so the
// apply would succeed and the NEXT read would address the collection endpoint
// instead of a row, leaving the resource permanently unreadable.
func requireID(row map[string]any) (string, error) {
	if row == nil {
		return "", fmt.Errorf("%w (leere Antwort)", errNoID)
	}
	id := idString(row["id"])
	if id == "" {
		return "", fmt.Errorf("%w (Feld \"id\" fehlt oder ist leer)", errNoID)
	}
	return id, nil
}

// nestedIDString reads row[parent][key], a JSON number inside a nested object,
// as a decimal string. null, absent and a missing parent all give
// types.StringNull(): CT serves a group's campusId as JSON null for "no campus",
// and 0 is a real campus, so "unset" must never be confused with "0".
func nestedIDString(row map[string]any, parent, key string) types.String {
	obj, _ := row[parent].(map[string]any)
	if s := idString(obj[key]); s != "" {
		return types.StringValue(s)
	}
	return types.StringNull()
}

// nestedBool reads row[parent][key] as a bool; absent or not a bool is
// types.BoolNull() (unknown to us), never false.
func nestedBool(row map[string]any, parent, key string) types.Bool {
	obj, _ := row[parent].(map[string]any)
	if b, ok := obj[key].(bool); ok {
		return types.BoolValue(b)
	}
	return types.BoolNull()
}
