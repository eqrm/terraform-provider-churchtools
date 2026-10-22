package ruleset

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// A ruleset file may carry reference markers where a host-specific id belongs:
//
//	{"__ctRef": true, "kind": "group",           "key": "bereich_kids"}
//	{"__ctRef": true, "kind": "campus",          "key": "mainz"}
//	{"__ctRef": true, "kind": "group-type",      "key": "group"}
//	{"__ctRef": true, "kind": "group-type-role", "groupType": "local_lead", "role": "leiter"}
//	{"__ctRef": true, "kind": "person",          "key": "<slug>"}
//
// (the shape ct-cli's `ref.*` helpers emit and ct-structure/rulesets/*.json
// carry; `person` is the host-bound marker planned for person.id operands). The
// provider replaces each marker with the INTEGER id its `refs` entry names. The
// refs key is "<kind>:<key>"; for group-type-role the key is "<groupType>/<role>",
// so "group-type-role:local_lead/leiter".

// Marker is one `__ctRef` object found in a ruleset.
type Marker struct {
	// RefKey is the key the marker needs in the `refs` map.
	RefKey string
}

// Supported marker kinds. Anything else is an error rather than a silently
// unresolved marker that would be PUT to ChurchTools as an object.
var markerKinds = map[string]bool{
	"group": true, "campus": true, "group-type": true, "group-type-role": true, "person": true,
}

// RefKeyKinds lists the kinds a `refs` key may start with.
func RefKeyKinds() []string {
	out := make([]string, 0, len(markerKinds))
	for k := range markerKinds {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func isMarker(m map[string]any) bool {
	b, ok := m["__ctRef"].(bool)
	return ok && b
}

func markerRefKey(m map[string]any) (string, error) {
	kind, _ := m["kind"].(string)
	if !markerKinds[kind] {
		return "", fmt.Errorf("Marker mit unbekanntem kind %q (erlaubt: %s)", kind, strings.Join(RefKeyKinds(), ", "))
	}
	str := func(field string) (string, error) {
		s, ok := m[field].(string)
		if !ok || s == "" {
			return "", fmt.Errorf("Marker kind %q braucht ein nicht leeres %q", kind, field)
		}
		return s, nil
	}
	if kind == "group-type-role" {
		gt, err := str("groupType")
		if err != nil {
			return "", err
		}
		role, err := str("role")
		if err != nil {
			return "", err
		}
		return kind + ":" + gt + "/" + role, nil
	}
	key, err := str("key")
	if err != nil {
		return "", err
	}
	return kind + ":" + key, nil
}

// Markers returns the refs key of every marker in the tree, with duplicates
// removed, in a deterministic order.
func Markers(v any) ([]Marker, error) {
	var out []Marker
	seen := map[string]bool{}
	var walk func(any) error
	walk = func(n any) error {
		switch t := n.(type) {
		case []any:
			for _, x := range t {
				if err := walk(x); err != nil {
					return err
				}
			}
		case map[string]any:
			if isMarker(t) {
				k, err := markerRefKey(t)
				if err != nil {
					return err
				}
				if !seen[k] {
					seen[k] = true
					out = append(out, Marker{RefKey: k})
				}
				return nil
			}
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if err := walk(t[k]); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(v); err != nil {
		return nil, err
	}
	return out, nil
}

var canonicalID = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)

// Resolve returns a copy of the tree with every marker replaced by the integer
// id from refs. A marker without a refs entry, or an entry that is not a
// non-negative integer, is an error. The input is not modified.
func Resolve(v any, refs map[string]string) (any, error) {
	var walk func(any) (any, error)
	walk = func(n any) (any, error) {
		switch t := n.(type) {
		case []any:
			out := make([]any, len(t))
			for i, x := range t {
				r, err := walk(x)
				if err != nil {
					return nil, err
				}
				out[i] = r
			}
			return out, nil
		case map[string]any:
			if isMarker(t) {
				k, err := markerRefKey(t)
				if err != nil {
					return nil, err
				}
				id, ok := refs[k]
				if !ok {
					return nil, fmt.Errorf("refs hat keinen Eintrag %q", k)
				}
				if !canonicalID.MatchString(id) {
					return nil, fmt.Errorf("refs[%q] = %q ist keine numerische ChurchTools-id", k, id)
				}
				return json.Number(id), nil
			}
			out := make(map[string]any, len(t))
			for k, x := range t {
				r, err := walk(x)
				if err != nil {
					return nil, err
				}
				out[k] = r
			}
			return out, nil
		}
		return n, nil
	}
	return walk(v)
}
