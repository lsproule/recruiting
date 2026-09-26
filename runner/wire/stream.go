package wire

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// The stream format a driver reads its arguments in and writes its result
// in. It is deliberately trivial to parse in any language with nothing but
// a byte cursor: every scalar is a token followed by a newline, a string is
// its byte length then the bytes, a list is its length then its elements,
// a map is its size then key/value pairs. Booleans are 1 and 0.
//
//	int, long   -> "42\n"
//	float       -> "1.5\n"           (shortest round-trip decimal)
//	bool        -> "1\n" or "0\n"
//	string      -> "5\nhello\n"      (byte length, newline, bytes, newline)
//	int[]       -> "3\n1\n2\n3\n"
//	int[][]     -> "2\n" + row + row (each row an int[])
//	map<...>    -> "2\n" + key value + key value
//
// ResultMarker separates whatever the candidate printed from the result the
// driver appends, so a stray debug print does not corrupt the answer.
const ResultMarker = "\n@@RESULT@@\n"

// Encode renders the arguments of one call in the stream format, in
// parameter order.
func Encode(sig Signature, args []any) []byte {
	var b bytes.Buffer
	for i, p := range sig.Params {
		encodeValue(&b, p.Type, args[i])
	}
	return b.Bytes()
}

// EncodeValue renders one value of a type in the stream format.
func EncodeValue(typ string, v any) []byte {
	var b bytes.Buffer
	encodeValue(&b, typ, v)
	return b.Bytes()
}

func encodeValue(b *bytes.Buffer, typ string, v any) {
	switch typ {
	case TypeInt, TypeLong:
		b.WriteString(strconv.FormatInt(toInt64(v), 10))
		b.WriteByte('\n')
	case TypeFloat:
		f, _ := v.(float64)
		b.WriteString(strconv.FormatFloat(f, 'g', -1, 64))
		b.WriteByte('\n')
	case TypeBool:
		if t, _ := v.(bool); t {
			b.WriteString("1\n")
		} else {
			b.WriteString("0\n")
		}
	case TypeString:
		s, _ := v.(string)
		b.WriteString(strconv.Itoa(len(s)))
		b.WriteByte('\n')
		b.WriteString(s)
		b.WriteByte('\n')
	default:
		switch {
		case IsList(typ):
			list, _ := v.([]any)
			b.WriteString(strconv.Itoa(len(list)))
			b.WriteByte('\n')
			for _, item := range list {
				encodeValue(b, ElementType(typ), item)
			}
		case IsMap(typ):
			m, _ := v.(map[string]any)
			keys := sortedKeys(m)
			b.WriteString(strconv.Itoa(len(keys)))
			b.WriteByte('\n')
			for _, k := range keys {
				encodeValue(b, TypeString, k)
				encodeValue(b, ElementType(typ), m[k])
			}
		}
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	return keys
}

// sortStrings is an insertion sort: maps here are small, and it keeps the
// package free of imports the harness image would otherwise need.
func sortStrings(a []string) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

// Decode reads one value of a type from what a driver wrote after the
// result marker. It refuses trailing bytes beyond whitespace, so a driver
// that wrote too much is a fault rather than a silent pass.
func Decode(typ string, data []byte) (any, error) {
	r := &reader{data: data}
	v, err := r.value(typ)
	if err != nil {
		return nil, err
	}
	if rest := bytes.TrimSpace(r.data[r.pos:]); len(rest) > 0 {
		return nil, fmt.Errorf("unexpected data after the result: %q", clipBytes(rest, 40))
	}
	return v, nil
}

type reader struct {
	data []byte
	pos  int
}

var errShort = errors.New("the result ended early")

func (r *reader) token() (string, error) {
	for r.pos < len(r.data) && isSpace(r.data[r.pos]) {
		r.pos++
	}
	if r.pos >= len(r.data) {
		return "", errShort
	}
	start := r.pos
	for r.pos < len(r.data) && !isSpace(r.data[r.pos]) {
		r.pos++
	}
	return string(r.data[start:r.pos]), nil
}

func isSpace(c byte) bool { return c == ' ' || c == '\n' || c == '\r' || c == '\t' }

func (r *reader) count() (int, error) {
	t, err := r.token()
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(t)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("want a length, got %q", t)
	}
	return n, nil
}

func (r *reader) value(typ string) (any, error) {
	switch typ {
	case TypeInt, TypeLong:
		t, err := r.token()
		if err != nil {
			return nil, err
		}
		n, err := strconv.ParseInt(t, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("want an integer, got %q", t)
		}
		return n, nil
	case TypeFloat:
		t, err := r.token()
		if err != nil {
			return nil, err
		}
		f, err := strconv.ParseFloat(strings.ToLower(t), 64)
		if err != nil {
			return nil, fmt.Errorf("want a number, got %q", t)
		}
		return f, nil
	case TypeBool:
		t, err := r.token()
		if err != nil {
			return nil, err
		}
		switch t {
		case "1", "true", "True":
			return true, nil
		case "0", "false", "False":
			return false, nil
		}
		return nil, fmt.Errorf("want a boolean, got %q", t)
	case TypeString:
		n, err := r.count()
		if err != nil {
			return nil, err
		}
		// One newline separates the length from the bytes.
		if r.pos < len(r.data) && r.data[r.pos] == '\r' {
			r.pos++
		}
		if r.pos < len(r.data) && r.data[r.pos] == '\n' {
			r.pos++
		}
		if r.pos+n > len(r.data) {
			return nil, errShort
		}
		s := string(r.data[r.pos : r.pos+n])
		r.pos += n
		return s, nil
	}
	switch {
	case IsList(typ):
		n, err := r.count()
		if err != nil {
			return nil, err
		}
		out := make([]any, 0, n)
		for i := 0; i < n; i++ {
			v, err := r.value(ElementType(typ))
			if err != nil {
				return nil, fmt.Errorf("item %d: %w", i, err)
			}
			out = append(out, v)
		}
		return out, nil
	case IsMap(typ):
		n, err := r.count()
		if err != nil {
			return nil, err
		}
		out := make(map[string]any, n)
		for i := 0; i < n; i++ {
			k, err := r.value(TypeString)
			if err != nil {
				return nil, fmt.Errorf("key %d: %w", i, err)
			}
			v, err := r.value(ElementType(typ))
			if err != nil {
				return nil, fmt.Errorf("value of %q: %w", k, err)
			}
			out[k.(string)] = v
		}
		return out, nil
	}
	return nil, fmt.Errorf("unknown type %q", typ)
}

func clipBytes(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "…"
	}
	return string(b)
}

// SplitResult separates what the program printed on its own from the
// result the driver appended after the last marker. ok is false when no
// marker was written: the function never returned.
func SplitResult(stdout []byte) (printed, result []byte, ok bool) {
	i := bytes.LastIndex(stdout, []byte(ResultMarker))
	if i < 0 {
		return stdout, nil, false
	}
	return stdout[:i], stdout[i+len(ResultMarker):], true
}
