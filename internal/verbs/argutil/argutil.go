// Package argutil provides the wire-shape coercers and validators every
// verb's ParseArgs needs. Args arrive on the daemon side as
// map[string]any, where every value the client put on the wire is
// string-typed (the CLI's parseFlags wraps each --key value as a string).
// argutil handles the translation back into typed Go values with consistent
// error semantics.
//
// Two layers:
//
//   - Layer 1 (ToInt / ToInt64 / ToBool / ToString) — pure coercers.
//     Each returns (typed-value, ok). They handle every shape the wire can
//     deliver, including the string form that bit ash git --op log when
//     the helper was forgotten in one verb.
//
//   - Layer 2 (Require* / Optional*) — coercion plus validation plus
//     consistent *proto.Error generation. These are the per-arg
//     workhorses that ParseArgs typically calls one-per-arg.
//
// The Layer 2 helpers always include the arg key in the error message so
// the agent can tell which arg was malformed.
package argutil

import (
	"strconv"
	"strings"

	"github.com/stazelabs/ash/internal/proto"
)

// -- Layer 1: pure coercers ------------------------------------------------

// ToInt accepts every integer-flavored Go type a wire decode could
// produce. The CLI's parseFlags hands us strings; ashmcp's json.Unmarshal
// hands us float64; but msgpack-go decodes small positive ints (0–127)
// into uint8 and slightly larger ones into uint16 / uint32 when the
// target is map[string]any. Without coverage of the full integer family,
// any caller that talks msgpack directly with Go-native ints (wirecmp,
// internal tooling, replay sessions reading ledger data) trips an
// `args: <name> must be a positive integer` envelope (ASH-148, ASH-149).
func ToInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int8:
		return int(n), true
	case int16:
		return int(n), true
	case int32:
		return int(n), true
	case int64:
		return int(n), true
	case uint:
		return int(n), true
	case uint8:
		return int(n), true
	case uint16:
		return int(n), true
	case uint32:
		return int(n), true
	case uint64:
		return int(n), true
	case float32:
		return int(n), true
	case float64:
		return int(n), true
	case string:
		i, err := strconv.Atoi(n)
		return i, err == nil
	}
	return 0, false
}

func ToInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int8:
		return int64(n), true
	case int16:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case uint:
		return int64(n), true
	case uint8:
		return int64(n), true
	case uint16:
		return int64(n), true
	case uint32:
		return int64(n), true
	case uint64:
		return int64(n), true
	case float32:
		return int64(n), true
	case float64:
		return int64(n), true
	case string:
		i, err := strconv.ParseInt(n, 10, 64)
		return i, err == nil
	}
	return 0, false
}

func ToBool(v any) (bool, bool) {
	switch n := v.(type) {
	case bool:
		return n, true
	case string:
		switch strings.ToLower(n) {
		case "true", "1", "yes":
			return true, true
		case "false", "0", "no":
			return false, true
		}
	}
	return false, false
}

func ToString(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}

// -- Layer 2: validating extractors ---------------------------------------

// RequireString reads a required string arg. Empty strings are rejected
// (the agent never means "" for a required arg; that's a malformed call).
func RequireString(in map[string]any, key string) (string, *proto.Error) {
	v, ok := in[key]
	if !ok {
		return "", &proto.Error{Code: "args", Msg: "missing required arg: " + key}
	}
	s, ok := ToString(v)
	if !ok || s == "" {
		return "", &proto.Error{Code: "args", Msg: key + " must be a non-empty string"}
	}
	return s, nil
}

// OptionalString reads an optional string with a default. Empty strings
// are accepted as a valid explicit value (e.g. exclude="" means "no
// exclude pattern").
func OptionalString(in map[string]any, key, def string) (string, *proto.Error) {
	v, ok := in[key]
	if !ok || v == nil {
		return def, nil
	}
	s, ok := ToString(v)
	if !ok {
		return "", &proto.Error{Code: "args", Msg: key + " must be a string"}
	}
	return s, nil
}

// OptionalNonEmptyString rejects explicit-but-empty values. Use for args
// where empty would be a typo, not an intentional clear (e.g. glob has a
// meaningful default of "**" and "" would be wrong).
func OptionalNonEmptyString(in map[string]any, key, def string) (string, *proto.Error) {
	v, ok := in[key]
	if !ok || v == nil {
		return def, nil
	}
	s, ok := ToString(v)
	if !ok || s == "" {
		return "", &proto.Error{Code: "args", Msg: key + " must be a non-empty string"}
	}
	return s, nil
}

// OptionalBool reads an optional bool with a default.
func OptionalBool(in map[string]any, key string, def bool) (bool, *proto.Error) {
	v, ok := in[key]
	if !ok || v == nil {
		return def, nil
	}
	b, ok := ToBool(v)
	if !ok {
		return false, &proto.Error{Code: "args", Msg: key + " must be a bool (true/false)"}
	}
	return b, nil
}

// OptionalPosInt reads an optional positive integer with a default. If
// hardCap > 0, values exceeding it are silently clamped to hardCap (the
// pattern used by every limit arg in the codebase).
func OptionalPosInt(in map[string]any, key string, def, hardCap int) (int, *proto.Error) {
	v, ok := in[key]
	if !ok || v == nil {
		return def, nil
	}
	n, ok := ToInt(v)
	if !ok || n <= 0 {
		return 0, &proto.Error{Code: "args", Msg: key + " must be a positive integer"}
	}
	if hardCap > 0 && n > hardCap {
		n = hardCap
	}
	return n, nil
}

// OptionalNonNegInt reads an optional non-negative integer with a default.
// 0 is a valid value (often "unlimited" in the verb's domain). hardCap
// works the same as OptionalPosInt.
func OptionalNonNegInt(in map[string]any, key string, def, hardCap int) (int, *proto.Error) {
	v, ok := in[key]
	if !ok || v == nil {
		return def, nil
	}
	n, ok := ToInt(v)
	if !ok || n < 0 {
		return 0, &proto.Error{Code: "args", Msg: key + " must be a non-negative integer"}
	}
	if hardCap > 0 && n > hardCap {
		n = hardCap
	}
	return n, nil
}

// OptionalEnum reads an optional string that must be one of allowed.
// Useful for args like type ∈ {any,file,dir,symlink} or case ∈
// {smart,sensitive,insensitive}.
func OptionalEnum(in map[string]any, key, def string, allowed []string) (string, *proto.Error) {
	v, ok := in[key]
	if !ok || v == nil {
		return def, nil
	}
	s, ok := ToString(v)
	if !ok {
		return "", &proto.Error{Code: "args", Msg: key + " must be a string"}
	}
	for _, a := range allowed {
		if s == a {
			return s, nil
		}
	}
	return "", &proto.Error{Code: "args", Msg: key + " must be one of: " + strings.Join(allowed, ", ")}
}
