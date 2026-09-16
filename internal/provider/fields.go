package provider

import "strconv"

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
