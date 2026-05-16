package argutil

import (
	"strings"
	"testing"
)

// -- Layer 1 coercers ----------------------------------------------------

// TestToInt covers every wire-shape the daemon could see. The string
// case is the one that bit ash git --op log when its local toInt was
// missing the arm; the int8/16/32, uint, uint8/16/32, float32 arms
// were added in ASH-149 after wirecmp surfaced that msgpack-go decodes
// small positive ints into uint8 / uint16 / uint32 when the target is
// map[string]any.
func TestToInt(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want int
		ok   bool
	}{
		{"int", int(42), 42, true},
		{"int8", int8(42), 42, true},
		{"int16", int16(42), 42, true},
		{"int32", int32(42), 42, true},
		{"int64", int64(42), 42, true},
		{"uint", uint(42), 42, true},
		{"uint8", uint8(42), 42, true},
		{"uint16", uint16(42), 42, true},
		{"uint32", uint32(42), 42, true},
		{"uint64", uint64(42), 42, true},
		{"float32", float32(42), 42, true},
		{"float64", float64(42), 42, true},
		{"string", "42", 42, true},
		{"negative_int8", int8(-7), -7, true},
		{"negative_string", "-7", -7, true},
		{"bad_string", "not a number", 0, false},
		{"bool", true, 0, false},
		{"nil", nil, 0, false},
		{"slice", []int{1, 2}, 0, false},
		{"map", map[string]int{"a": 1}, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ToInt(c.in)
			if ok != c.ok || (ok && got != c.want) {
				t.Errorf("ToInt(%v)=(%d, %v); want (%d, %v)", c.in, got, ok, c.want, c.ok)
			}
		})
	}
}

func TestToInt64(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want int64
		ok   bool
	}{
		{"int", int(7), 7, true},
		{"int8", int8(7), 7, true},
		{"int16", int16(7), 7, true},
		{"int32", int32(7), 7, true},
		{"int64", int64(7), 7, true},
		{"uint", uint(7), 7, true},
		{"uint8", uint8(7), 7, true},
		{"uint16", uint16(7), 7, true},
		{"uint32", uint32(7), 7, true},
		{"uint64", uint64(7), 7, true},
		{"float32", float32(7), 7, true},
		{"float64", float64(7), 7, true},
		{"max_int64_string", "9223372036854775807", 9223372036854775807, true},
		{"bad_string", "not a number", 0, false},
		{"bool", true, 0, false},
		{"nil", nil, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ToInt64(c.in)
			if ok != c.ok || (ok && got != c.want) {
				t.Errorf("ToInt64(%v)=(%d, %v); want (%d, %v)", c.in, got, ok, c.want, c.ok)
			}
		})
	}
}

func TestToBool(t *testing.T) {
	for _, in := range []string{"true", "True", "TRUE", "1", "yes", "YES"} {
		v, ok := ToBool(in)
		if !ok || !v {
			t.Errorf("ToBool(%q)=(%v,%v); want (true,true)", in, v, ok)
		}
	}
	for _, in := range []string{"false", "False", "0", "no", "No"} {
		v, ok := ToBool(in)
		if !ok || v {
			t.Errorf("ToBool(%q)=(%v,%v); want (false,true)", in, v, ok)
		}
	}
	for _, in := range []any{"maybe", "", 1, nil} {
		if _, ok := ToBool(in); ok {
			t.Errorf("ToBool(%v) should reject", in)
		}
	}
	if v, ok := ToBool(true); !ok || !v {
		t.Errorf("ToBool(true bool): %v %v", v, ok)
	}
}

// -- Layer 2 validators --------------------------------------------------

func TestRequireString(t *testing.T) {
	t.Run("present", func(t *testing.T) {
		s, perr := RequireString(map[string]any{"path": "src"}, "path")
		if perr != nil || s != "src" {
			t.Errorf("got (%q, %+v)", s, perr)
		}
	})
	t.Run("missing", func(t *testing.T) {
		_, perr := RequireString(map[string]any{}, "path")
		if perr == nil || perr.Code != "args" || !strings.Contains(perr.Msg, "missing required arg: path") {
			t.Errorf("expected missing-arg error, got %+v", perr)
		}
	})
	t.Run("empty rejected", func(t *testing.T) {
		_, perr := RequireString(map[string]any{"path": ""}, "path")
		if perr == nil || perr.Code != "args" {
			t.Errorf("expected error for empty required string, got %+v", perr)
		}
	})
	t.Run("non-string rejected", func(t *testing.T) {
		_, perr := RequireString(map[string]any{"path": 42}, "path")
		if perr == nil {
			t.Error("expected error for non-string value")
		}
	})
}

func TestOptionalString(t *testing.T) {
	t.Run("absent uses default", func(t *testing.T) {
		s, perr := OptionalString(map[string]any{}, "exclude", "default")
		if perr != nil || s != "default" {
			t.Errorf("got (%q, %+v)", s, perr)
		}
	})
	t.Run("nil uses default", func(t *testing.T) {
		s, _ := OptionalString(map[string]any{"exclude": nil}, "exclude", "default")
		if s != "default" {
			t.Errorf("nil should use default, got %q", s)
		}
	})
	t.Run("empty allowed", func(t *testing.T) {
		s, perr := OptionalString(map[string]any{"exclude": ""}, "exclude", "default")
		if perr != nil || s != "" {
			t.Errorf("empty optional should pass through, got (%q, %+v)", s, perr)
		}
	})
}

func TestOptionalNonEmptyString(t *testing.T) {
	t.Run("absent uses default", func(t *testing.T) {
		s, _ := OptionalNonEmptyString(map[string]any{}, "glob", "**")
		if s != "**" {
			t.Errorf("got %q", s)
		}
	})
	t.Run("empty rejected", func(t *testing.T) {
		_, perr := OptionalNonEmptyString(map[string]any{"glob": ""}, "glob", "**")
		if perr == nil {
			t.Error("explicit-but-empty should reject")
		}
	})
}

func TestOptionalBool(t *testing.T) {
	t.Run("absent uses default", func(t *testing.T) {
		b, _ := OptionalBool(map[string]any{}, "ignored", true)
		if b != true {
			t.Errorf("got %v want true", b)
		}
	})
	t.Run("string accepted", func(t *testing.T) {
		b, perr := OptionalBool(map[string]any{"ignored": "false"}, "ignored", true)
		if perr != nil || b != false {
			t.Errorf("got (%v, %+v)", b, perr)
		}
	})
	t.Run("garbage rejected", func(t *testing.T) {
		_, perr := OptionalBool(map[string]any{"ignored": "maybe"}, "ignored", true)
		if perr == nil {
			t.Error("expected error for non-bool string")
		}
	})
}

// TestOptionalPosInt_StringInput is the regression for the bug that
// kicked off this extraction: a verb's local toInt missing the string
// arm caused --limit 5 to be rejected. Now we get this shape for free
// across every verb.
func TestOptionalPosInt_StringInput(t *testing.T) {
	n, perr := OptionalPosInt(map[string]any{"limit": "5"}, "limit", 20, 200)
	if perr != nil {
		t.Fatalf("string limit rejected: %+v", perr)
	}
	if n != 5 {
		t.Errorf("got %d want 5", n)
	}
}

func TestOptionalPosInt_Defaults(t *testing.T) {
	n, _ := OptionalPosInt(map[string]any{}, "limit", 20, 200)
	if n != 20 {
		t.Errorf("default not used: %d", n)
	}
}

func TestOptionalPosInt_ClampsToHardCap(t *testing.T) {
	n, _ := OptionalPosInt(map[string]any{"limit": 9999}, "limit", 20, 200)
	if n != 200 {
		t.Errorf("expected clamp to 200, got %d", n)
	}
}

func TestOptionalPosInt_RejectsZeroAndNegative(t *testing.T) {
	for _, bad := range []any{0, -1, "0", "-5"} {
		_, perr := OptionalPosInt(map[string]any{"limit": bad}, "limit", 20, 200)
		if perr == nil {
			t.Errorf("expected error for limit=%v", bad)
		}
	}
}

func TestOptionalNonNegInt_AllowsZero(t *testing.T) {
	n, perr := OptionalNonNegInt(map[string]any{"max_depth": 0}, "max_depth", 0, 0)
	if perr != nil || n != 0 {
		t.Errorf("got (%d, %+v)", n, perr)
	}
}

func TestOptionalNonNegInt_RejectsNegative(t *testing.T) {
	_, perr := OptionalNonNegInt(map[string]any{"max_depth": -1}, "max_depth", 0, 0)
	if perr == nil {
		t.Error("expected error for negative max_depth")
	}
}

func TestOptionalEnum(t *testing.T) {
	allowed := []string{"any", "file", "dir", "symlink"}
	t.Run("absent uses default", func(t *testing.T) {
		s, _ := OptionalEnum(map[string]any{}, "type", "any", allowed)
		if s != "any" {
			t.Errorf("got %q", s)
		}
	})
	t.Run("valid value", func(t *testing.T) {
		s, perr := OptionalEnum(map[string]any{"type": "file"}, "type", "any", allowed)
		if perr != nil || s != "file" {
			t.Errorf("got (%q, %+v)", s, perr)
		}
	})
	t.Run("invalid rejected", func(t *testing.T) {
		_, perr := OptionalEnum(map[string]any{"type": "weird"}, "type", "any", allowed)
		if perr == nil {
			t.Error("expected enum violation")
		}
	})
}

// Sanity: every Layer-2 helper should produce an error message that
// includes the arg key, so an agent reading a stack of errors knows
// which arg was malformed.
func TestErrorMessagesIncludeKey(t *testing.T) {
	if _, perr := RequireString(map[string]any{}, "myarg"); perr == nil || !strings.Contains(perr.Msg, "myarg") {
		t.Errorf("RequireString missing key in msg: %+v", perr)
	}
	if _, perr := OptionalPosInt(map[string]any{"myarg": -1}, "myarg", 1, 0); perr == nil || !strings.Contains(perr.Msg, "myarg") {
		t.Errorf("OptionalPosInt missing key in msg: %+v", perr)
	}
	if _, perr := OptionalBool(map[string]any{"myarg": "maybe"}, "myarg", false); perr == nil || !strings.Contains(perr.Msg, "myarg") {
		t.Errorf("OptionalBool missing key in msg: %+v", perr)
	}
	if _, perr := OptionalEnum(map[string]any{"myarg": "x"}, "myarg", "y", []string{"y", "z"}); perr == nil || !strings.Contains(perr.Msg, "myarg") {
		t.Errorf("OptionalEnum missing key in msg: %+v", perr)
	}
}
