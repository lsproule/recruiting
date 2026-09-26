package wire

import (
	"fmt"
	"strings"
)

// Languages a function problem can be solved in, in registry order. The
// platform's own registry (internal/domain) is derived from this list, so
// the harness and the app cannot drift apart.
var Languages = []string{"python", "javascript", "ruby", "php", "go", "java", "csharp", "cpp", "c", "rust"}

// Layout is where a language's files go and how they are built and run
// inside the sandbox. {work} is the work dir, {bin} the binary a compile
// produces. Concat marks languages whose driver is appended to the
// candidate's source in one file rather than compiled beside it.
type Layout struct {
	Candidate string
	Driver    string
	Concat    bool
	Compile   []string
	Run       []string
}

// Layouts is the function-mode file layout per language.
var Layouts = map[string]Layout{
	"python": {Candidate: "solution.py", Driver: "main.py",
		Compile: []string{"python3", "-I", "-m", "py_compile", "{work}/solution.py"},
		Run:     []string{"python3", "-I", "-u", "{work}/main.py"}},
	"javascript": {Candidate: "main.js", Concat: true,
		Compile: []string{"node", "--check", "{work}/main.js"},
		Run:     []string{"node", "--stack-size=2048", "{work}/main.js"}},
	"ruby": {Candidate: "main.rb", Concat: true,
		Compile: []string{"ruby", "-c", "{work}/main.rb"},
		Run:     []string{"ruby", "{work}/main.rb"}},
	"php": {Candidate: "main.php", Concat: true,
		Compile: []string{"php", "-l", "{work}/main.php"},
		Run:     []string{"php", "-d", "error_reporting=E_ALL", "-d", "memory_limit=-1", "{work}/main.php"}},
	"go": {Candidate: "solution.go", Driver: "driver.go",
		Compile: []string{"go", "build", "-o", "{bin}", "."},
		Run:     []string{"{bin}"}},
	"java": {Candidate: "Solution.java", Driver: "Main.java",
		Compile: []string{"javac", "-d", "{work}", "Solution.java", "Main.java"},
		Run:     []string{"java", "-Xss64m", "-XX:+UseSerialGC", "-XX:TieredStopAtLevel=1", "-cp", "{work}", "Main"}},
	"csharp": {Candidate: "Solution.cs", Driver: "Program.cs",
		Compile: []string{"dotnet", "build", "--nologo", "-c", "Release", "-o", "{work}/out"},
		Run:     []string{"dotnet", "{work}/out/candidate.dll"}},
	"cpp": {Candidate: "solution.cpp", Driver: "main.cpp",
		Compile: []string{"g++", "-O2", "-std=c++20", "-o", "{bin}", "main.cpp"},
		Run:     []string{"{bin}"}},
	"c": {Candidate: "solution.c", Driver: "main.c",
		Compile: []string{"gcc", "-O2", "-std=c17", "-o", "{bin}", "main.c", "-lm"},
		Run:     []string{"{bin}"}},
	"rust": {Candidate: "solution.rs", Driver: "main.rs",
		Compile: []string{"rustc", "-O", "--edition", "2021", "-o", "{bin}", "main.rs"},
		Run:     []string{"{bin}"}},
}

// NativeType is how a language spells one of the signature types.
func NativeType(lang, typ string) string {
	table, ok := nativeTypes[lang]
	if !ok {
		return typ
	}
	if t, ok := table[typ]; ok {
		return t
	}
	return typ
}

var nativeTypes = map[string]map[string]string{
	"python": {
		TypeInt: "int", TypeLong: "int", TypeFloat: "float", TypeBool: "bool", TypeString: "str",
		TypeIntList: "list[int]", TypeLongList: "list[int]", TypeFloatList: "list[float]", TypeBoolList: "list[bool]",
		TypeStringList: "list[str]", TypeIntMatrix: "list[list[int]]", TypeStringGrid: "list[list[str]]",
		TypeMapInt: "dict[str, int]", TypeMapString: "dict[str, str]",
	},
	"javascript": {
		TypeInt: "number", TypeLong: "number", TypeFloat: "number", TypeBool: "boolean", TypeString: "string",
		TypeIntList: "number[]", TypeLongList: "number[]", TypeFloatList: "number[]", TypeBoolList: "boolean[]",
		TypeStringList: "string[]", TypeIntMatrix: "number[][]", TypeStringGrid: "string[][]",
		TypeMapInt: "Object<string, number>", TypeMapString: "Object<string, string>",
	},
	"ruby": {
		TypeInt: "Integer", TypeLong: "Integer", TypeFloat: "Float", TypeBool: "Boolean", TypeString: "String",
		TypeIntList: "Array<Integer>", TypeLongList: "Array<Integer>", TypeFloatList: "Array<Float>", TypeBoolList: "Array<Boolean>",
		TypeStringList: "Array<String>", TypeIntMatrix: "Array<Array<Integer>>", TypeStringGrid: "Array<Array<String>>",
		TypeMapInt: "Hash{String => Integer}", TypeMapString: "Hash{String => String}",
	},
	"php": {
		TypeInt: "int", TypeLong: "int", TypeFloat: "float", TypeBool: "bool", TypeString: "string",
		TypeIntList: "array", TypeLongList: "array", TypeFloatList: "array", TypeBoolList: "array",
		TypeStringList: "array", TypeIntMatrix: "array", TypeStringGrid: "array",
		TypeMapInt: "array", TypeMapString: "array",
	},
	"go": {
		TypeInt: "int", TypeLong: "int64", TypeFloat: "float64", TypeBool: "bool", TypeString: "string",
		TypeIntList: "[]int", TypeLongList: "[]int64", TypeFloatList: "[]float64", TypeBoolList: "[]bool",
		TypeStringList: "[]string", TypeIntMatrix: "[][]int", TypeStringGrid: "[][]string",
		TypeMapInt: "map[string]int", TypeMapString: "map[string]string",
	},
	"java": {
		TypeInt: "int", TypeLong: "long", TypeFloat: "double", TypeBool: "boolean", TypeString: "String",
		TypeIntList: "int[]", TypeLongList: "long[]", TypeFloatList: "double[]", TypeBoolList: "boolean[]",
		TypeStringList: "String[]", TypeIntMatrix: "int[][]", TypeStringGrid: "String[][]",
		TypeMapInt: "Map<String, Integer>", TypeMapString: "Map<String, String>",
	},
	"csharp": {
		TypeInt: "int", TypeLong: "long", TypeFloat: "double", TypeBool: "bool", TypeString: "string",
		TypeIntList: "int[]", TypeLongList: "long[]", TypeFloatList: "double[]", TypeBoolList: "bool[]",
		TypeStringList: "string[]", TypeIntMatrix: "int[][]", TypeStringGrid: "string[][]",
		TypeMapInt: "Dictionary<string, int>", TypeMapString: "Dictionary<string, string>",
	},
	"cpp": {
		TypeInt: "int", TypeLong: "long long", TypeFloat: "double", TypeBool: "bool", TypeString: "std::string",
		TypeIntList: "std::vector<int>", TypeLongList: "std::vector<long long>", TypeFloatList: "std::vector<double>",
		TypeBoolList: "std::vector<bool>", TypeStringList: "std::vector<std::string>",
		TypeIntMatrix: "std::vector<std::vector<int>>", TypeStringGrid: "std::vector<std::vector<std::string>>",
		TypeMapInt: "std::map<std::string, int>", TypeMapString: "std::map<std::string, std::string>",
	},
	"c": {
		TypeInt: "int", TypeLong: "long long", TypeFloat: "double", TypeBool: "bool", TypeString: "const char *",
		TypeIntList: "const int *", TypeLongList: "const long long *", TypeFloatList: "const double *",
		TypeBoolList: "const bool *", TypeStringList: "const char **",
		TypeIntMatrix: "const int **", TypeStringGrid: "const char ***",
		TypeMapInt: "const char **", TypeMapString: "const char **",
	},
	"rust": {
		TypeInt: "i32", TypeLong: "i64", TypeFloat: "f64", TypeBool: "bool", TypeString: "String",
		TypeIntList: "Vec<i32>", TypeLongList: "Vec<i64>", TypeFloatList: "Vec<f64>", TypeBoolList: "Vec<bool>",
		TypeStringList: "Vec<String>", TypeIntMatrix: "Vec<Vec<i32>>", TypeStringGrid: "Vec<Vec<String>>",
		TypeMapInt: "std::collections::HashMap<String, i32>", TypeMapString: "std::collections::HashMap<String, String>",
	},
}

// CSupports reports whether C can express the signature: C has no
// containers, so a list is passed as a pointer and a length, a matrix as
// rows with their lengths, a map as parallel key and value arrays, and a
// returned list comes back through an out-parameter. A matrix or a map
// cannot be returned.
func CSupports(sig Signature) bool {
	switch sig.Returns {
	case TypeIntMatrix, TypeStringGrid, TypeMapInt, TypeMapString:
		return false
	}
	return true
}

// Supports reports whether a language can express the signature.
func Supports(lang string, sig Signature) bool {
	if _, ok := Layouts[lang]; !ok {
		return false
	}
	if lang == "c" {
		return CSupports(sig)
	}
	return true
}

// Stub is the empty function a candidate starts from, in one language,
// with the platform's own conventions for that language.
func Stub(lang string, sig Signature) string {
	params := make([]string, 0, len(sig.Params))
	switch lang {
	case "python":
		for _, p := range sig.Params {
			params = append(params, p.Name+": "+NativeType(lang, p.Type))
		}
		return fmt.Sprintf("def %s(%s) -> %s:\n    pass\n", sig.Name, strings.Join(params, ", "), NativeType(lang, sig.Returns))
	case "javascript":
		var doc strings.Builder
		doc.WriteString("/**\n")
		for _, p := range sig.Params {
			fmt.Fprintf(&doc, " * @param {%s} %s\n", NativeType(lang, p.Type), p.Name)
			params = append(params, p.Name)
		}
		fmt.Fprintf(&doc, " * @returns {%s}\n */\n", NativeType(lang, sig.Returns))
		return doc.String() + fmt.Sprintf("function %s(%s) {\n  \n}\n", sig.Name, strings.Join(params, ", "))
	case "ruby":
		var doc strings.Builder
		for _, p := range sig.Params {
			fmt.Fprintf(&doc, "# @param %s [%s]\n", p.Name, NativeType(lang, p.Type))
			params = append(params, p.Name)
		}
		fmt.Fprintf(&doc, "# @return [%s]\n", NativeType(lang, sig.Returns))
		return doc.String() + fmt.Sprintf("def %s(%s)\n  \nend\n", sig.Name, strings.Join(params, ", "))
	case "php":
		var doc strings.Builder
		doc.WriteString("<?php\n/**\n")
		for _, p := range sig.Params {
			fmt.Fprintf(&doc, " * @param %s $%s\n", phpDocType(p.Type), p.Name)
			params = append(params, NativeType(lang, p.Type)+" $"+p.Name)
		}
		fmt.Fprintf(&doc, " * @return %s\n */\n", phpDocType(sig.Returns))
		return doc.String() + fmt.Sprintf("function %s(%s): %s {\n    \n}\n", sig.Name, strings.Join(params, ", "), NativeType(lang, sig.Returns))
	case "go":
		for _, p := range sig.Params {
			params = append(params, p.Name+" "+NativeType(lang, p.Type))
		}
		return fmt.Sprintf("package main\n\nfunc %s(%s) %s {\n\t\n}\n", sig.Name, strings.Join(params, ", "), NativeType(lang, sig.Returns))
	case "java":
		for _, p := range sig.Params {
			params = append(params, NativeType(lang, p.Type)+" "+p.Name)
		}
		return fmt.Sprintf("import java.util.*;\n\nclass Solution {\n    public static %s %s(%s) {\n        \n    }\n}\n",
			NativeType(lang, sig.Returns), sig.Name, strings.Join(params, ", "))
	case "csharp":
		for _, p := range sig.Params {
			params = append(params, NativeType(lang, p.Type)+" "+p.Name)
		}
		return fmt.Sprintf("using System;\nusing System.Collections.Generic;\nusing System.Linq;\n\nclass Solution {\n    public static %s %s(%s) {\n        \n    }\n}\n",
			NativeType(lang, sig.Returns), sig.Name, strings.Join(params, ", "))
	case "cpp":
		for _, p := range sig.Params {
			params = append(params, NativeType(lang, p.Type)+" "+p.Name)
		}
		return fmt.Sprintf("#include <string>\n#include <vector>\n#include <map>\n#include <algorithm>\n\n%s %s(%s) {\n    \n}\n",
			NativeType(lang, sig.Returns), sig.Name, strings.Join(params, ", "))
	case "c":
		return "#include <stdio.h>\n#include <stdlib.h>\n#include <string.h>\n#include <stdbool.h>\n\n" + cPrototype(sig) + " {\n    \n}\n"
	case "rust":
		for _, p := range sig.Params {
			params = append(params, p.Name+": "+NativeType(lang, p.Type))
		}
		return fmt.Sprintf("use std::collections::HashMap;\n\nfn %s(%s) -> %s {\n    \n}\n", sig.Name, strings.Join(params, ", "), NativeType(lang, sig.Returns))
	}
	return ""
}

func phpDocType(typ string) string {
	switch typ {
	case TypeIntList, TypeLongList:
		return "int[]"
	case TypeFloatList:
		return "float[]"
	case TypeBoolList:
		return "bool[]"
	case TypeStringList:
		return "string[]"
	case TypeIntMatrix:
		return "int[][]"
	case TypeStringGrid:
		return "string[][]"
	case TypeMapInt:
		return "array<string, int>"
	case TypeMapString:
		return "array<string, string>"
	}
	return NativeType("php", typ)
}

// cPrototype is the C signature: containers become pointer-and-length
// pairs, a returned list gains an out-parameter for its length.
func cPrototype(sig Signature) string {
	params := make([]string, 0, len(sig.Params)*3)
	for _, p := range sig.Params {
		params = append(params, cParams(p)...)
	}
	ret := NativeType("c", sig.Returns)
	switch sig.Returns {
	case TypeString:
		ret = "char *"
	case TypeIntList:
		ret, params = "int *", append(params, "int *out_len")
	case TypeLongList:
		ret, params = "long long *", append(params, "int *out_len")
	case TypeFloatList:
		ret, params = "double *", append(params, "int *out_len")
	case TypeBoolList:
		ret, params = "bool *", append(params, "int *out_len")
	case TypeStringList:
		ret, params = "char **", append(params, "int *out_len")
	}
	if len(params) == 0 {
		params = []string{"void"}
	}
	ret = strings.TrimRight(ret, " ")
	if !strings.HasSuffix(ret, "*") {
		ret += " "
	}
	return ret + sig.Name + "(" + strings.Join(params, ", ") + ")"
}

// cParams is how one parameter is spelled in C.
func cParams(p Param) []string {
	t := NativeType("c", p.Type)
	switch p.Type {
	case TypeIntMatrix:
		return []string{t + p.Name, "int " + p.Name + "_len", "const int *" + p.Name + "_lens"}
	case TypeStringGrid:
		return []string{t + p.Name, "int " + p.Name + "_len", "const int *" + p.Name + "_lens"}
	case TypeMapInt:
		return []string{"const char **" + p.Name + "_keys", "const int *" + p.Name + "_values", "int " + p.Name + "_len"}
	case TypeMapString:
		return []string{"const char **" + p.Name + "_keys", "const char **" + p.Name + "_values", "int " + p.Name + "_len"}
	}
	if IsList(p.Type) {
		return []string{t + p.Name, "int " + p.Name + "_len"}
	}
	if p.Type == TypeString {
		return []string{t + p.Name}
	}
	return []string{t + " " + p.Name}
}
