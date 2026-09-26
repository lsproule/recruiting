package wire_test

import (
	"encoding/json"
	"strings"
	"testing"

	"recruiting/runner/wire"
)

var sig = wire.Signature{
	Name: "solve",
	Params: []wire.Param{
		{Name: "nums", Type: wire.TypeIntList}, {Name: "label", Type: wire.TypeString},
		{Name: "ratio", Type: wire.TypeFloat}, {Name: "counts", Type: wire.TypeMapInt},
		{Name: "grid", Type: wire.TypeIntMatrix}, {Name: "flag", Type: wire.TypeBool},
	},
	Returns: wire.TypeStringList,
}

func TestArgsRoundTripThroughTheStream(t *testing.T) {
	args, err := sig.ArgsOf(json.RawMessage(`[[1,-2,3], "héllo world\nline", 2.5, {"b": 2, "a": 1}, [[1,2],[3]], true]`))
	if err != nil {
		t.Fatal(err)
	}
	stream := wire.Encode(sig, args)
	want := "3\n1\n-2\n3\n17\nhéllo world\nline\n2.5\n2\n1\na\n1\n1\nb\n2\n2\n2\n1\n2\n1\n3\n1\n"
	if string(stream) != want {
		t.Fatalf("stream =\n%q\nwant\n%q", stream, want)
	}
	// Each argument decodes back to what was encoded.
	rest := stream
	for i, p := range sig.Params {
		one := wire.EncodeValue(p.Type, args[i])
		if !strings.HasPrefix(string(rest), string(one)) {
			t.Fatalf("argument %d is not next in the stream", i)
		}
		got, err := wire.Decode(p.Type, one)
		if err != nil {
			t.Fatalf("decode %s: %v", p.Type, err)
		}
		if !wire.ValuesEqual(p.Type, got, args[i]) || wire.CanonicalJSON(got) != wire.CanonicalJSON(args[i]) {
			t.Fatalf("argument %d = %s, want %s", i, wire.CanonicalJSON(got), wire.CanonicalJSON(args[i]))
		}
		rest = rest[len(one):]
	}
}

func TestArgsAreCheckedAgainstTheSignature(t *testing.T) {
	for _, raw := range []string{
		`[[1,2]]`,                                 // too few
		`[[1,"x"], "s", 1.0, {}, [], true]`,       // a string in an int list
		`[[1], "s", "1", {}, [], true]`,           // a string for a float
		`[[1], "s", 1.0, {"a": 1.5}, [], true]`,   // a float in an int map
		`[[1], "s", 1.0, {}, [[1],[2]], "yes"]`,   // a string for a bool
		`[[99999999999], "s", 1.0, {}, [], true]`, // out of 32-bit range
		`{"nums": [1]}`,                           // not an array
	} {
		if _, err := sig.ArgsOf(json.RawMessage(raw)); err == nil {
			t.Errorf("%s was accepted", raw)
		}
	}
}

func TestCanonicalJSONSortsKeysAndSpellsFloatsOnce(t *testing.T) {
	v, err := wire.DecodeTyped(wire.TypeMapInt, json.RawMessage(`{"z": 1, "a": 2}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := wire.CanonicalJSON(v); got != `{"a":2,"z":1}` {
		t.Fatalf("canonical map = %s", got)
	}
	f, _ := wire.DecodeTyped(wire.TypeFloat, json.RawMessage(`3`))
	if got := wire.CanonicalJSON(f); got != "3.0" {
		t.Fatalf("canonical float = %s", got)
	}
	f2, _ := wire.Decode(wire.TypeFloat, []byte("1e+20\n"))
	if got := wire.CanonicalJSON(f2); got != "1e+20" {
		t.Fatalf("canonical big float = %s", got)
	}
}

func TestFloatsCompareWithinTolerance(t *testing.T) {
	if !wire.ValuesEqual(wire.TypeFloat, 0.1+0.2, 0.3) {
		t.Error("0.1+0.2 should equal 0.3")
	}
	if wire.ValuesEqual(wire.TypeFloat, 1.0, 1.01) {
		t.Error("1.0 should not equal 1.01")
	}
	got, _ := wire.Decode(wire.TypeFloatList, []byte("2\n0.30000000000000004\n1\n"))
	want, _ := wire.DecodeTyped(wire.TypeFloatList, json.RawMessage(`[0.3, 1]`))
	if !wire.ValuesEqual(wire.TypeFloatList, got, want) {
		t.Error("float lists should compare within tolerance")
	}
	if wire.ValuesEqual(wire.TypeIntList, mustDecode(wire.TypeIntList, "2\n1\n2\n"), mustJSON(wire.TypeIntList, `[1,2,3]`)) {
		t.Error("lists of different length compared equal")
	}
}

func mustDecode(typ, s string) any {
	v, err := wire.Decode(typ, []byte(s))
	if err != nil {
		panic(err)
	}
	return v
}

func mustJSON(typ, s string) any {
	v, err := wire.DecodeTyped(typ, json.RawMessage(s))
	if err != nil {
		panic(err)
	}
	return v
}

func TestDecodeRefusesTrailingData(t *testing.T) {
	if _, err := wire.Decode(wire.TypeInt, []byte("1\n2\n")); err == nil {
		t.Error("trailing data was accepted")
	}
	if _, err := wire.Decode(wire.TypeIntList, []byte("3\n1\n2\n")); err == nil {
		t.Error("a short list was accepted")
	}
}

func TestSplitResultTakesTheLastMarker(t *testing.T) {
	printed, res, ok := wire.SplitResult([]byte("debug\n@@RESULT@@ not it\n@@RESULT@@\n4\n"))
	if !ok || string(res) != "4\n" || string(printed) != "debug\n@@RESULT@@ not it" {
		t.Fatalf("split = %q %q %v", printed, res, ok)
	}
	if _, _, ok := wire.SplitResult([]byte("nothing")); ok {
		t.Error("a missing marker read as a result")
	}
}

func TestEveryLanguageHasAStubAndADriver(t *testing.T) {
	for _, lang := range wire.Languages {
		stub := wire.Stub(lang, sig)
		if !strings.Contains(stub, "solve") {
			t.Errorf("%s stub does not name the function: %q", lang, stub)
		}
		if _, ok := wire.Layouts[lang]; !ok {
			t.Errorf("%s has no layout", lang)
		}
		driver, err := wire.Driver(lang, sig)
		if err != nil {
			t.Errorf("%s driver: %v", lang, err)
			continue
		}
		if !strings.Contains(driver, "@@RESULT@@") || !strings.Contains(driver, "solve") {
			t.Errorf("%s driver is missing the marker or the call", lang)
		}
	}
	// C cannot return a map; the driver refuses rather than compile garbage.
	mapSig := wire.Signature{Name: "count", Returns: wire.TypeMapInt}
	if _, err := wire.Driver("c", mapSig); err == nil {
		t.Error("a C driver returning a map was generated")
	}
	if wire.Supports("c", mapSig) || !wire.Supports("cpp", mapSig) {
		t.Error("Supports disagrees with the C rule")
	}
}

func TestCStubSpellsContainersAsPointersAndLengths(t *testing.T) {
	s := wire.Signature{
		Name:    "top",
		Params:  []wire.Param{{Name: "counts", Type: wire.TypeMapInt}, {Name: "grid", Type: wire.TypeIntMatrix}, {Name: "k", Type: wire.TypeInt}},
		Returns: wire.TypeStringList,
	}
	stub := wire.Stub("c", s)
	want := "char **top(const char **counts_keys, const int *counts_values, int counts_len, const int **grid, int grid_len, const int *grid_lens, int k, int *out_len)"
	if !strings.Contains(stub, want) {
		t.Fatalf("C stub = %q, want it to contain %q", stub, want)
	}
}
