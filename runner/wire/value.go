// Package wire is the contract between the platform and the sandbox harness
// for function problems: the signature a candidate implements, the typed
// values a test case passes and expects, the byte stream those values travel
// to and from a driver as, and the driver and stub source for every language.
// It has no dependencies, because the harness is built inside each sandbox
// image from this package alone.
package wire

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Value types a signature may use. Every language the platform runs maps
// each of them onto its own natural type; the harness moves values between
// the two without the candidate writing any parsing.
const (
	TypeInt        = "int"
	TypeLong       = "long"
	TypeFloat      = "float"
	TypeBool       = "bool"
	TypeString     = "string"
	TypeIntList    = "int[]"
	TypeLongList   = "long[]"
	TypeFloatList  = "float[]"
	TypeBoolList   = "bool[]"
	TypeStringList = "string[]"
	TypeIntMatrix  = "int[][]"
	TypeStringGrid = "string[][]"
	TypeMapInt     = "map<string,int>"
	TypeMapString  = "map<string,string>"
)

// SignatureTypes is every type a parameter or a return may declare, in the
// order the authoring screen offers them.
var SignatureTypes = []string{
	TypeInt, TypeLong, TypeFloat, TypeBool, TypeString,
	TypeIntList, TypeLongList, TypeFloatList, TypeBoolList, TypeStringList,
	TypeIntMatrix, TypeStringGrid, TypeMapInt, TypeMapString,
}

// MaxSignatureParams bounds a signature; a function of more than eight
// arguments is a problem statement that wants a struct.
const MaxSignatureParams = 8

// Param is one argument of the function a candidate implements.
type Param struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Signature is the function a function problem asks for: its name, the
// arguments in order, and what it returns.
type Signature struct {
	Name    string  `json:"name"`
	Params  []Param `json:"params"`
	Returns string  `json:"returns"`
}

// ArgsOf decodes a test case's arguments, one per parameter, and checks each
// against its declared type. The JSON is what the import document carries
// and what the harness receives, so a case that would confuse the harness
// is refused here first.
func (s Signature) ArgsOf(raw json.RawMessage) ([]any, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("args are required")
	}
	var args []json.RawMessage
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("args must be a JSON array: %v", err)
	}
	if len(args) != len(s.Params) {
		return nil, fmt.Errorf("args holds %d values, the signature takes %d", len(args), len(s.Params))
	}
	out := make([]any, 0, len(args))
	for i, a := range args {
		v, err := DecodeTyped(s.Params[i].Type, a)
		if err != nil {
			return nil, fmt.Errorf("argument %q: %v", s.Params[i].Name, err)
		}
		out = append(out, v)
	}
	return out, nil
}

// DecodeTyped reads one JSON value as the declared type, refusing anything
// that does not fit. The Go value it returns is the canonical in-memory
// shape: int64 for integers, float64 for floats, []any for lists,
// map[string]any for maps.
func DecodeTyped(typ string, raw json.RawMessage) (any, error) {
	var v any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("not valid JSON: %v", err)
	}
	return coerce(typ, v)
}

func coerce(typ string, v any) (any, error) {
	switch typ {
	case TypeInt, TypeLong:
		n, ok := v.(json.Number)
		if !ok {
			return nil, fmt.Errorf("want an integer, got %s", describe(v))
		}
		i, err := n.Int64()
		if err != nil {
			return nil, fmt.Errorf("want an integer, got %s", n.String())
		}
		if typ == TypeInt && (i > math.MaxInt32 || i < math.MinInt32) {
			return nil, fmt.Errorf("%d does not fit a 32-bit int; use long", i)
		}
		return i, nil
	case TypeFloat:
		switch n := v.(type) {
		case json.Number:
			f, err := n.Float64()
			if err != nil {
				return nil, fmt.Errorf("want a number, got %s", n.String())
			}
			return f, nil
		}
		return nil, fmt.Errorf("want a number, got %s", describe(v))
	case TypeBool:
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("want true or false, got %s", describe(v))
		}
		return b, nil
	case TypeString:
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("want a string, got %s", describe(v))
		}
		return s, nil
	case TypeIntList, TypeLongList, TypeFloatList, TypeBoolList, TypeStringList, TypeIntMatrix, TypeStringGrid:
		list, ok := v.([]any)
		if !ok {
			return nil, fmt.Errorf("want a list, got %s", describe(v))
		}
		elem := ElementType(typ)
		out := make([]any, 0, len(list))
		for i, item := range list {
			c, err := coerce(elem, item)
			if err != nil {
				return nil, fmt.Errorf("item %d: %v", i, err)
			}
			out = append(out, c)
		}
		return out, nil
	case TypeMapInt, TypeMapString:
		m, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("want an object, got %s", describe(v))
		}
		elem := ElementType(typ)
		out := make(map[string]any, len(m))
		for k, item := range m {
			c, err := coerce(elem, item)
			if err != nil {
				return nil, fmt.Errorf("key %q: %v", k, err)
			}
			out[k] = c
		}
		return out, nil
	}
	return nil, fmt.Errorf("unknown type %q", typ)
}

// ElementType is what a list or map holds; the type itself for a scalar.
func ElementType(typ string) string {
	switch typ {
	case TypeIntList:
		return TypeInt
	case TypeLongList:
		return TypeLong
	case TypeFloatList:
		return TypeFloat
	case TypeBoolList:
		return TypeBool
	case TypeStringList:
		return TypeString
	case TypeIntMatrix:
		return TypeIntList
	case TypeStringGrid:
		return TypeStringList
	case TypeMapInt:
		return TypeInt
	case TypeMapString:
		return TypeString
	}
	return typ
}

// IsList and IsMap classify a type.
func IsList(typ string) bool { return strings.HasSuffix(typ, "[]") }
func IsMap(typ string) bool  { return strings.HasPrefix(typ, "map<") }

func describe(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case json.Number:
		return "a number"
	case string:
		return "a string"
	case bool:
		return "a boolean"
	case []any:
		return "a list"
	case map[string]any:
		return "an object"
	}
	return "something else"
}

// CanonicalJSON renders a decoded value in one spelling, so two equal values
// always render the same: map keys sorted, floats in the shortest form that
// round-trips, no whitespace. It is what an expected value is stored as and
// what a result is shown as.
func CanonicalJSON(v any) string {
	var b strings.Builder
	writeCanonical(&b, v)
	return b.String()
}

func writeCanonical(b *strings.Builder, v any) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case int:
		b.WriteString(strconv.Itoa(x))
	case float64:
		b.WriteString(FormatFloat(x))
	case json.Number:
		b.WriteString(x.String())
	case string:
		enc, _ := json.Marshal(x)
		b.Write(enc)
	case []any:
		b.WriteByte('[')
		for i, item := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			writeCanonical(b, item)
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			enc, _ := json.Marshal(k)
			b.Write(enc)
			b.WriteByte(':')
			writeCanonical(b, x[k])
		}
		b.WriteByte('}')
	default:
		enc, _ := json.Marshal(x)
		b.Write(enc)
	}
}

// FormatFloat is the one spelling of a float the platform uses: the shortest
// decimal that round-trips, with a ".0" so an integral float still reads as
// one.
func FormatFloat(f float64) string {
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return "null"
	}
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	return s
}

// FloatTolerance is how far a returned float may sit from the expected one
// and still pass, relative to the larger magnitude with a floor of one, so
// a formula computed in a different order is not marked wrong.
const FloatTolerance = 1e-6

// ValuesEqual compares a returned value against an expected one of the same
// declared type: exactly for everything but floats, which compare within
// FloatTolerance wherever they sit in the structure.
func ValuesEqual(typ string, got, want any) bool {
	switch typ {
	case TypeFloat:
		g, ok1 := got.(float64)
		w, ok2 := want.(float64)
		if !ok1 || !ok2 {
			return false
		}
		scale := math.Max(1, math.Max(math.Abs(g), math.Abs(w)))
		return math.Abs(g-w) <= FloatTolerance*scale
	case TypeInt, TypeLong:
		return toInt64(got) == toInt64(want) && isInt(got) && isInt(want)
	case TypeBool, TypeString:
		return got == want
	}
	if IsList(typ) {
		g, ok1 := got.([]any)
		w, ok2 := want.([]any)
		if !ok1 || !ok2 || len(g) != len(w) {
			return false
		}
		elem := ElementType(typ)
		for i := range g {
			if !ValuesEqual(elem, g[i], w[i]) {
				return false
			}
		}
		return true
	}
	if IsMap(typ) {
		g, ok1 := got.(map[string]any)
		w, ok2 := want.(map[string]any)
		if !ok1 || !ok2 || len(g) != len(w) {
			return false
		}
		elem := ElementType(typ)
		for k, wv := range w {
			gv, ok := g[k]
			if !ok || !ValuesEqual(elem, gv, wv) {
				return false
			}
		}
		return true
	}
	return false
}

func isInt(v any) bool {
	switch v.(type) {
	case int64, int:
		return true
	}
	return false
}

func toInt64(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	}
	return 0
}

// Describe renders the signature the way the statement shows it to every
// candidate, language-neutral: `solve(nums: int[], target: int) -> int`.
func (s Signature) Describe() string {
	parts := make([]string, 0, len(s.Params))
	for _, p := range s.Params {
		parts = append(parts, p.Name+": "+p.Type)
	}
	return s.Name + "(" + strings.Join(parts, ", ") + ") -> " + s.Returns
}

// Normalize trims the names and lower-cases the types.
func (s *Signature) Normalize() {
	s.Name = strings.TrimSpace(s.Name)
	s.Returns = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s.Returns), " ", ""))
	for i := range s.Params {
		s.Params[i].Name = strings.TrimSpace(s.Params[i].Name)
		s.Params[i].Type = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s.Params[i].Type), " ", ""))
	}
}

// Contains reports whether list holds v.
func Contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
