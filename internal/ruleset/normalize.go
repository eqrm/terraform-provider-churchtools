// Package ruleset normalises ChurchTools dynamic-group rulesets and resolves the
// `__ctRef` markers a ruleset file may carry.
//
// The normalizer is a 1:1 port of ct-cli's `normalizeRuleset`
// (src/engine/dynamic.ts), so ct and this provider agree on what "the same
// ruleset" means. ChurchTools hands rulesets back with cosmetic labels,
// inconsistent int/string leaf types and read-only timestamps inside a
// single-element array; normalising both the desired and the live side to one
// canonical form is what keeps drift real instead of spurious.
//
// Trees are decoded with json.Decoder.UseNumber: float64 would lose ids above
// 2^53 and turn 1 into 1.0. Maps are map[string]any, arrays []any, numbers
// json.Number.
package ruleset

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"sort"
	"strconv"
	"strings"
)

var readOnlyKeys = map[string]bool{
	"dynamicGroupUpdateStarted":  true,
	"dynamicGroupUpdateFinished": true,
}

// maxSafeInteger is JavaScript's Number.MAX_SAFE_INTEGER (2^53 - 1), the bound
// ct-cli uses for coercing integer strings.
const maxSafeInteger = 1<<53 - 1

// Parse decodes JSON with numbers kept as json.Number and refuses trailing data.
func Parse(raw string) (any, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("unexpected data after the JSON value")
	}
	return v, nil
}

// NormalizeJSON parses and normalizes a ruleset document.
func NormalizeJSON(raw string) (map[string]any, error) {
	v, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	return Normalize(v)
}

// Normalize canonicalises a ruleset for diffing: it unwraps the `[RuleSet]`
// array and the `{dynamicGroupRuleSet: [...]}` PUT envelope, drops the read-only
// timestamps, and, inside `query` only, strips cosmetic dterm labels and
// coerces canonical integer strings to numbers. `process`, `shorty` and
// `description` pass through untouched. The input is not modified.
func Normalize(rule any) (map[string]any, error) {
	r := rule
	if r == nil {
		r = map[string]any{}
	}
	if arr, ok := r.([]any); ok {
		if len(arr) == 0 {
			r = map[string]any{}
		} else {
			r = arr[0]
		}
	}
	obj, ok := r.(map[string]any)
	if !ok {
		if r == nil {
			obj = map[string]any{}
		} else {
			return nil, fmt.Errorf("a ruleset must be a JSON object, got %s", kindOf(r))
		}
	}
	if inner, has := obj["dynamicGroupRuleSet"]; has && truthyObject(inner) {
		if arr, isArr := inner.([]any); isArr {
			if len(arr) == 0 {
				inner = map[string]any{}
			} else {
				inner = arr[0]
			}
		}
		next, ok := inner.(map[string]any)
		if !ok {
			if inner != nil {
				return nil, fmt.Errorf("dynamicGroupRuleSet must hold a ruleset object, got %s", kindOf(inner))
			}
			next = map[string]any{}
		}
		obj = next
	}
	base := make(map[string]any, len(obj))
	for k, v := range obj {
		if readOnlyKeys[k] {
			continue
		}
		base[k] = deepCopy(v)
	}
	if q, has := base["query"]; has {
		base["query"] = coerceScalars(stripCosmeticLabels(q))
	}
	return base, nil
}

// truthyObject mirrors the JS `obj.dynamicGroupRuleSet && typeof === "object"`
// guard (arrays are objects; null, strings and numbers are not).
func truthyObject(v any) bool {
	switch v.(type) {
	case map[string]any, []any:
		return true
	}
	return false
}

func kindOf(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "a string"
	case bool:
		return "a boolean"
	case json.Number:
		return "a number"
	case []any:
		return "an array"
	}
	return fmt.Sprintf("%T", v)
}

func deepCopy(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = deepCopy(x)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = deepCopy(x)
		}
		return out
	}
	return v
}

// isCosmeticLabel: a dterm label is pure display text when it is null, a
// string, or an object carrying nothing but a `title`. Anything else is kept. In
// particular `stereotype` ["groupmembership"] is NOT cosmetic: it makes
// ChurchTools evaluate the wrapped group condition per person, and dropping it
// turns a negated group condition into one that excludes no one. An allowlist,
// so a label key ChurchTools adds later is kept rather than silently stripped.
func isCosmeticLabel(label any) bool {
	switch t := label.(type) {
	case nil, string:
		return true
	case map[string]any:
		for k := range t {
			if k != "title" {
				return false
			}
		}
		return true
	}
	return false
}

// labelWrapper reports whether obj is exactly {dterm: [label, expr]}.
func labelWrapper(obj map[string]any) (label, expr any, ok bool) {
	if len(obj) != 1 {
		return nil, nil, false
	}
	arr, isArr := obj["dterm"].([]any)
	if !isArr || len(arr) != 2 {
		return nil, nil, false
	}
	return arr[0], arr[1], true
}

func stripCosmeticLabels(node any) any {
	switch t := node.(type) {
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = stripCosmeticLabels(x)
		}
		return out
	case map[string]any:
		if label, expr, ok := labelWrapper(t); ok {
			if !isCosmeticLabel(label) {
				return map[string]any{"dterm": []any{deepCopy(label), stripCosmeticLabels(expr)}}
			}
			return stripCosmeticLabels(expr)
		}
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = stripCosmeticLabels(x)
		}
		return out
	}
	return node
}

// canonicalIntString reports whether s is a canonical integer string
// (-?[1-9][0-9]* or 0) within the safe-integer range, and returns it.
func canonicalIntString(s string) (int64, bool) {
	if s == "0" {
		return 0, true
	}
	d := strings.TrimPrefix(s, "-")
	if d == "" || d[0] < '1' || d[0] > '9' {
		return 0, false
	}
	for i := 0; i < len(d); i++ {
		if d[i] < '0' || d[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n > maxSafeInteger || n < -maxSafeInteger {
		return 0, false
	}
	return n, true
}

// coerceScalars turns canonical integer strings into numbers (CT is int/string
// inconsistent for `var` values). Leading-zero strings ("01067") and strings
// beyond 2^53 stay strings. A kept dterm label is free text and is copied
// verbatim; only the wrapped expression is coerced.
func coerceScalars(node any) any {
	switch t := node.(type) {
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = coerceScalars(x)
		}
		return out
	case map[string]any:
		if label, expr, ok := labelWrapper(t); ok {
			return map[string]any{"dterm": []any{deepCopy(label), coerceScalars(expr)}}
		}
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = coerceScalars(x)
		}
		return out
	case string:
		if n, ok := canonicalIntString(t); ok {
			return json.Number(strconv.FormatInt(n, 10))
		}
	}
	return node
}

// CanonicalNumber renders a JSON number the way JavaScript would after a
// parse/stringify round trip: integral values as plain digits (1.0 -> 1,
// 1e2 -> 100), others as the shortest float form.
func CanonicalNumber(n json.Number) string {
	r, ok := new(big.Rat).SetString(string(n))
	if !ok {
		return string(n)
	}
	if r.IsInt() {
		return r.Num().String()
	}
	f, _ := r.Float64()
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// Canonical serialises a tree deterministically: keys sorted, no HTML escaping,
// numbers in CanonicalNumber form. Equal trees give equal strings.
func Canonical(v any) (string, error) {
	var buf bytes.Buffer
	if err := writeCanonical(&buf, v); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func writeCanonical(buf *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if t {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case json.Number:
		buf.WriteString(CanonicalNumber(t))
	case string:
		return writeString(buf, t)
	case []any:
		buf.WriteByte('[')
		for i, x := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonical(buf, x); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeString(buf, k); err != nil {
				return err
			}
			buf.WriteByte(':')
			if err := writeCanonical(buf, t[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return fmt.Errorf("unsupported JSON value of type %T", v)
	}
	return nil
}

func writeString(buf *bytes.Buffer, s string) error {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return err
	}
	buf.Write(bytes.TrimRight(b.Bytes(), "\n"))
	return nil
}

// Equal is a deep equality of two decoded trees: object key order does not
// matter, array order does, numbers compare by value (1 == 1.0).
func Equal(a, b any) bool {
	switch x := a.(type) {
	case nil:
		return b == nil
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case string:
		y, ok := b.(string)
		return ok && x == y
	case json.Number:
		y, ok := b.(json.Number)
		return ok && CanonicalNumber(x) == CanonicalNumber(y)
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !Equal(x[i], y[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, xv := range x {
			yv, has := y[k]
			if !has || !Equal(xv, yv) {
				return false
			}
		}
		return true
	}
	return false
}

// PutBody is the body PUT /dynamicgroups/{id}/ruleset expects: an object whose
// `dynamicGroupRuleSet` property is a single-element array (#77 in ct-cli; CT
// rejects both the bare object and the bare array).
func PutBody(rs map[string]any) map[string]any {
	return map[string]any{"dynamicGroupRuleSet": []any{rs}}
}
