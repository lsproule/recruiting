package wire

import (
	"fmt"
	"strings"
)

// Driver is the source that turns a candidate's function into a program
// the harness can run: it reads the arguments from stdin in the stream
// format, calls the function, and writes the result after ResultMarker.
// For a Concat layout the text is appended to the candidate's own source;
// otherwise it is written beside it under Layout.Driver.
func Driver(lang string, sig Signature) (string, error) {
	if !Supports(lang, sig) {
		return "", fmt.Errorf("%s cannot express %s", lang, sig.Describe())
	}
	switch lang {
	case "python":
		return pythonDriver(sig), nil
	case "javascript":
		return javascriptDriver(sig), nil
	case "ruby":
		return rubyDriver(sig), nil
	case "php":
		return phpDriver(sig), nil
	case "go":
		return goDriver(sig), nil
	case "java":
		return javaDriver(sig), nil
	case "csharp":
		return csharpDriver(sig), nil
	case "cpp":
		return cppDriver(sig), nil
	case "c":
		return cDriver(sig), nil
	case "rust":
		return rustDriver(sig), nil
	}
	return "", fmt.Errorf("no driver for %s", lang)
}

// The dynamic languages read and write by a type string at run time, so
// their drivers are one runtime plus a call site; the typed languages get
// one function per type and a generated call site naming the right ones.

func pythonDriver(sig Signature) string {
	args := make([]string, 0, len(sig.Params))
	for _, p := range sig.Params {
		args = append(args, fmt.Sprintf("_read(%q)", p.Type))
	}
	return fmt.Sprintf(pythonRuntime, sig.Name, strings.Join(args, ", "), sig.Returns)
}

const pythonRuntime = `import sys, os
sys.setrecursionlimit(1000000)
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from solution import %[1]s as _fn

_data = sys.stdin.buffer.read()
_pos = 0
_ws = b' \n\r\t'


def _tok():
    global _pos
    while _pos < len(_data) and _data[_pos] in _ws:
        _pos += 1
    s = _pos
    while _pos < len(_data) and _data[_pos] not in _ws:
        _pos += 1
    return _data[s:_pos].decode()


def _str():
    global _pos
    n = int(_tok())
    if _pos < len(_data) and _data[_pos:_pos + 1] == b'\r':
        _pos += 1
    _pos += 1
    v = _data[_pos:_pos + n].decode('utf-8', 'replace')
    _pos += n
    return v


def _read(t):
    if t == 'int' or t == 'long':
        return int(_tok())
    if t == 'float':
        return float(_tok())
    if t == 'bool':
        return _tok() == '1'
    if t == 'string':
        return _str()
    if t.endswith('[]'):
        n = int(_tok())
        e = t[:-2]
        return [_read(e) for _ in range(n)]
    n = int(_tok())
    e = t[t.index(',') + 1:-1]
    d = {}
    for _ in range(n):
        k = _str()
        d[k] = _read(e)
    return d


_out = []


def _write(t, v):
    if t == 'int' or t == 'long':
        _out.append(str(int(v)).encode())
    elif t == 'float':
        _out.append(repr(float(v)).encode())
    elif t == 'bool':
        _out.append(b'1' if v else b'0')
    elif t == 'string':
        b = str(v).encode('utf-8')
        _out.append(str(len(b)).encode())
        _out.append(b)
    elif t.endswith('[]'):
        e = t[:-2]
        v = list(v)
        _out.append(str(len(v)).encode())
        for x in v:
            _write(e, x)
    else:
        e = t[t.index(',') + 1:-1]
        items = list(dict(v).items())
        _out.append(str(len(items)).encode())
        for k, x in items:
            _write('string', k)
            _write(e, x)


_args = [%[2]s]
_res = _fn(*_args)
sys.stdout.flush()
_write(%[3]q, _res)
sys.stdout.buffer.write(b'\n@@RESULT@@\n' + b'\n'.join(_out) + b'\n')
sys.stdout.buffer.flush()
`

func javascriptDriver(sig Signature) string {
	args := make([]string, 0, len(sig.Params))
	for _, p := range sig.Params {
		args = append(args, fmt.Sprintf("__read(%q)", p.Type))
	}
	return fmt.Sprintf(javascriptRuntime, sig.Name, strings.Join(args, ", "), sig.Returns)
}

const javascriptRuntime = `
;(function () {
  const fs = require('fs');
  const data = fs.readFileSync(0);
  let pos = 0;
  const ws = (c) => c === 32 || c === 10 || c === 13 || c === 9;
  const tok = () => { while (pos < data.length && ws(data[pos])) pos++; const s = pos; while (pos < data.length && !ws(data[pos])) pos++; return data.toString('utf8', s, pos); };
  const str = () => { const n = parseInt(tok(), 10); if (pos < data.length && data[pos] === 13) pos++; pos++; const v = data.toString('utf8', pos, pos + n); pos += n; return v; };
  const __read = (t) => {
    if (t === 'int' || t === 'long') return parseInt(tok(), 10);
    if (t === 'float') return parseFloat(tok());
    if (t === 'bool') return tok() === '1';
    if (t === 'string') return str();
    if (t.endsWith('[]')) { const n = parseInt(tok(), 10); const e = t.slice(0, -2); const a = []; for (let i = 0; i < n; i++) a.push(__read(e)); return a; }
    const n = parseInt(tok(), 10); const e = t.slice(t.indexOf(',') + 1, -1); const o = {}; for (let i = 0; i < n; i++) { const k = str(); o[k] = __read(e); } return o;
  };
  const out = [];
  const raw = (s) => { out.push(Buffer.from(String(s), 'utf8')); };
  const __write = (t, v) => {
    if (t === 'int' || t === 'long') raw(typeof v === 'bigint' ? v.toString() : String(Math.trunc(Number(v))));
    else if (t === 'float') raw(String(Number(v)));
    else if (t === 'bool') raw(v ? '1' : '0');
    else if (t === 'string') { const b = Buffer.from(String(v), 'utf8'); raw(b.length); out.push(b); }
    else if (t.endsWith('[]')) { const e = t.slice(0, -2); const a = Array.from(v); raw(a.length); for (const x of a) __write(e, x); }
    else { const e = t.slice(t.indexOf(',') + 1, -1); const entries = v instanceof Map ? Array.from(v.entries()) : Object.entries(v); raw(entries.length); for (const [k, x] of entries) { __write('string', k); __write(e, x); } }
  };
  const args = [%[2]s];
  const res = %[1]s(...args);
  __write(%[3]q, res);
  const parts = [Buffer.from('\n@@RESULT@@\n')];
  for (const b of out) { parts.push(b, Buffer.from('\n')); }
  fs.writeSync(1, Buffer.concat(parts));
})();
`

func rubyDriver(sig Signature) string {
	args := make([]string, 0, len(sig.Params))
	for _, p := range sig.Params {
		args = append(args, fmt.Sprintf("__read(%q)", p.Type))
	}
	return fmt.Sprintf(rubyRuntime, sig.Name, strings.Join(args, ", "), sig.Returns)
}

const rubyRuntime = `

$__data = $stdin.binmode.read
$__pos = 0
def __ws(c)
  c == " " || c == "\n" || c == "\r" || c == "\t"
end
def __tok
  $__pos += 1 while $__pos < $__data.bytesize && __ws($__data.byteslice($__pos))
  s = $__pos
  $__pos += 1 while $__pos < $__data.bytesize && !__ws($__data.byteslice($__pos))
  $__data.byteslice(s, $__pos - s)
end
def __str
  n = __tok.to_i
  $__pos += 1 if $__data.byteslice($__pos) == "\r"
  $__pos += 1
  v = $__data.byteslice($__pos, n).force_encoding('UTF-8')
  $__pos += n
  v
end
def __read(t)
  case t
  when 'int', 'long' then __tok.to_i
  when 'float' then __tok.to_f
  when 'bool' then __tok == '1'
  when 'string' then __str
  else
    if t.end_with?('[]')
      n = __tok.to_i
      e = t[0..-3]
      Array.new(n) { __read(e) }
    else
      n = __tok.to_i
      e = t[(t.index(',') + 1)..-2]
      h = {}
      n.times do
        k = __str
        h[k] = __read(e)
      end
      h
    end
  end
end
$__out = []
def __write(t, v)
  case t
  when 'int', 'long' then $__out << v.to_i.to_s
  when 'float' then $__out << v.to_f.to_s
  when 'bool' then $__out << (v ? '1' : '0')
  when 'string'
    b = v.to_s.b
    $__out << b.bytesize.to_s
    $__out << b
  else
    if t.end_with?('[]')
      e = t[0..-3]
      a = v.to_a
      $__out << a.length.to_s
      a.each { |x| __write(e, x) }
    else
      e = t[(t.index(',') + 1)..-2]
      h = v.to_h
      $__out << h.length.to_s
      h.each { |k, x| __write('string', k.to_s); __write(e, x) }
    end
  end
end
__args = [%[2]s]
__res = %[1]s(*__args)
$stdout.flush
__write(%[3]q, __res)
$stdout.binmode
$stdout.write("\n@@RESULT@@\n".b + $__out.map(&:b).join("\n".b) + "\n".b)
$stdout.flush
`

func phpDriver(sig Signature) string {
	args := make([]string, 0, len(sig.Params))
	for _, p := range sig.Params {
		args = append(args, fmt.Sprintf("__read(%q)", p.Type))
	}
	return fmt.Sprintf(phpRuntime, sig.Name, strings.Join(args, ", "), sig.Returns)
}

const phpRuntime = `
?>
<?php
$__data = file_get_contents('php://stdin');
$__pos = 0;
function __ws($c) { return $c === ' ' || $c === "\n" || $c === "\r" || $c === "\t"; }
function __tok() {
    global $__data, $__pos;
    $n = strlen($__data);
    while ($__pos < $n && __ws($__data[$__pos])) $__pos++;
    $s = $__pos;
    while ($__pos < $n && !__ws($__data[$__pos])) $__pos++;
    return substr($__data, $s, $__pos - $s);
}
function __str() {
    global $__data, $__pos;
    $n = intval(__tok());
    if ($__pos < strlen($__data) && $__data[$__pos] === "\r") $__pos++;
    $__pos++;
    $v = substr($__data, $__pos, $n);
    $__pos += $n;
    return $v === false ? '' : $v;
}
function __read($t) {
    if ($t === 'int' || $t === 'long') return intval(__tok());
    if ($t === 'float') return floatval(__tok());
    if ($t === 'bool') return __tok() === '1';
    if ($t === 'string') return __str();
    if (substr($t, -2) === '[]') {
        $n = intval(__tok()); $e = substr($t, 0, -2); $a = [];
        for ($i = 0; $i < $n; $i++) $a[] = __read($e);
        return $a;
    }
    $n = intval(__tok()); $e = substr($t, strpos($t, ',') + 1, -1); $m = [];
    for ($i = 0; $i < $n; $i++) { $k = __str(); $m[$k] = __read($e); }
    return $m;
}
$__out = [];
function __write($t, $v) {
    global $__out;
    if ($t === 'int' || $t === 'long') $__out[] = strval(intval($v));
    elseif ($t === 'float') $__out[] = sprintf('%%.17G', floatval($v));
    elseif ($t === 'bool') $__out[] = $v ? '1' : '0';
    elseif ($t === 'string') { $s = strval($v); $__out[] = strval(strlen($s)); $__out[] = $s; }
    elseif (substr($t, -2) === '[]') {
        $e = substr($t, 0, -2); $a = array_values((array)$v); $__out[] = strval(count($a));
        foreach ($a as $x) __write($e, $x);
    } else {
        $e = substr($t, strpos($t, ',') + 1, -1); $m = (array)$v; $__out[] = strval(count($m));
        foreach ($m as $k => $x) { __write('string', strval($k)); __write($e, $x); }
    }
}
$__args = [%[2]s];
$__res = %[1]s(...$__args);
flush();
__write(%[3]q, $__res);
echo "\n@@RESULT@@\n" . implode("\n", $__out) . "\n";
`

// typedCall builds the argument declarations and the call for a statically
// typed language from per-type reader names. decl formats one declaration
// from (variable, reader expression).
func typedCall(sig Signature, reader func(typ string) string, decl func(name, expr string) string) (decls string, call string) {
	var b strings.Builder
	names := make([]string, 0, len(sig.Params))
	for i, p := range sig.Params {
		name := fmt.Sprintf("a%d", i)
		names = append(names, name)
		b.WriteString(decl(name, reader(p.Type)))
	}
	return b.String(), sig.Name + "(" + strings.Join(names, ", ") + ")"
}

// typedName is the suffix shared by the typed languages' reader and writer
// functions: rdIntList, wrMapString, and so on.
func typedName(typ string) string {
	switch typ {
	case TypeInt:
		return "Int"
	case TypeLong:
		return "Long"
	case TypeFloat:
		return "Float"
	case TypeBool:
		return "Bool"
	case TypeString:
		return "String"
	case TypeIntList:
		return "IntList"
	case TypeLongList:
		return "LongList"
	case TypeFloatList:
		return "FloatList"
	case TypeBoolList:
		return "BoolList"
	case TypeStringList:
		return "StringList"
	case TypeIntMatrix:
		return "IntMatrix"
	case TypeStringGrid:
		return "StringGrid"
	case TypeMapInt:
		return "MapInt"
	case TypeMapString:
		return "MapString"
	}
	return ""
}

func goDriver(sig Signature) string {
	decls, call := typedCall(sig,
		func(t string) string { return "rd" + typedName(t) + "()" },
		func(n, e string) string { return "\t" + n + " := " + e + "\n" })
	return fmt.Sprintf(goRuntime, decls, call, "wr"+typedName(sig.Returns))
}

const goRuntime = `package main

import (
	"bufio"
	"io"
	"os"
	"strconv"
)

var rdData []byte
var rdPos int

func rdIsSpace(c byte) bool { return c == ' ' || c == '\n' || c == '\r' || c == '\t' }

func rdTok() string {
	for rdPos < len(rdData) && rdIsSpace(rdData[rdPos]) {
		rdPos++
	}
	s := rdPos
	for rdPos < len(rdData) && !rdIsSpace(rdData[rdPos]) {
		rdPos++
	}
	return string(rdData[s:rdPos])
}
func rdInt() int         { n, _ := strconv.Atoi(rdTok()); return n }
func rdLong() int64      { n, _ := strconv.ParseInt(rdTok(), 10, 64); return n }
func rdFloat() float64   { f, _ := strconv.ParseFloat(rdTok(), 64); return f }
func rdBool() bool       { return rdTok() == "1" }
func rdString() string {
	n := rdInt()
	if rdPos < len(rdData) && rdData[rdPos] == '\r' {
		rdPos++
	}
	rdPos++
	s := string(rdData[rdPos : rdPos+n])
	rdPos += n
	return s
}
func rdIntList() []int {
	out := make([]int, rdInt())
	for i := range out {
		out[i] = rdInt()
	}
	return out
}
func rdLongList() []int64 {
	out := make([]int64, rdInt())
	for i := range out {
		out[i] = rdLong()
	}
	return out
}
func rdFloatList() []float64 {
	out := make([]float64, rdInt())
	for i := range out {
		out[i] = rdFloat()
	}
	return out
}
func rdBoolList() []bool {
	out := make([]bool, rdInt())
	for i := range out {
		out[i] = rdBool()
	}
	return out
}
func rdStringList() []string {
	out := make([]string, rdInt())
	for i := range out {
		out[i] = rdString()
	}
	return out
}
func rdIntMatrix() [][]int {
	out := make([][]int, rdInt())
	for i := range out {
		out[i] = rdIntList()
	}
	return out
}
func rdStringGrid() [][]string {
	out := make([][]string, rdInt())
	for i := range out {
		out[i] = rdStringList()
	}
	return out
}
func rdMapInt() map[string]int {
	n := rdInt()
	out := make(map[string]int, n)
	for i := 0; i < n; i++ {
		k := rdString()
		out[k] = rdInt()
	}
	return out
}
func rdMapString() map[string]string {
	n := rdInt()
	out := make(map[string]string, n)
	for i := 0; i < n; i++ {
		k := rdString()
		out[k] = rdString()
	}
	return out
}

var wrOut *bufio.Writer

func wrRaw(s string)      { wrOut.WriteString(s); wrOut.WriteByte('\n') }
func wrInt(v int)         { wrRaw(strconv.Itoa(v)) }
func wrLong(v int64)      { wrRaw(strconv.FormatInt(v, 10)) }
func wrFloat(v float64)   { wrRaw(strconv.FormatFloat(v, 'g', -1, 64)) }
func wrBool(v bool) {
	if v {
		wrRaw("1")
	} else {
		wrRaw("0")
	}
}
func wrString(s string) { wrRaw(strconv.Itoa(len(s))); wrOut.WriteString(s); wrOut.WriteByte('\n') }
func wrIntList(v []int) {
	wrInt(len(v))
	for _, x := range v {
		wrInt(x)
	}
}
func wrLongList(v []int64) {
	wrInt(len(v))
	for _, x := range v {
		wrLong(x)
	}
}
func wrFloatList(v []float64) {
	wrInt(len(v))
	for _, x := range v {
		wrFloat(x)
	}
}
func wrBoolList(v []bool) {
	wrInt(len(v))
	for _, x := range v {
		wrBool(x)
	}
}
func wrStringList(v []string) {
	wrInt(len(v))
	for _, x := range v {
		wrString(x)
	}
}
func wrIntMatrix(v [][]int) {
	wrInt(len(v))
	for _, x := range v {
		wrIntList(x)
	}
}
func wrStringGrid(v [][]string) {
	wrInt(len(v))
	for _, x := range v {
		wrStringList(x)
	}
}
func wrMapInt(v map[string]int) {
	wrInt(len(v))
	for k, x := range v {
		wrString(k)
		wrInt(x)
	}
}
func wrMapString(v map[string]string) {
	wrInt(len(v))
	for k, x := range v {
		wrString(k)
		wrString(x)
	}
}

func main() {
	rdData, _ = io.ReadAll(os.Stdin)
%[1]s	res := %[2]s
	os.Stdout.WriteString("\n@@RESULT@@\n")
	wrOut = bufio.NewWriter(os.Stdout)
	%[3]s(res)
	wrOut.Flush()
}
`

func javaDriver(sig Signature) string {
	decls, call := typedCall(sig,
		func(t string) string { return "rd" + typedName(t) + "()" },
		func(n, e string) string { return "        var " + n + " = " + e + ";\n" })
	return fmt.Sprintf(javaRuntime, decls, "Solution."+call, "wr"+typedName(sig.Returns))
}

const javaRuntime = `import java.io.*;
import java.util.*;
import java.nio.charset.StandardCharsets;

public class Main {
    static byte[] data;
    static int pos;

    static boolean ws(byte c) { return c == ' ' || c == '\n' || c == '\r' || c == '\t'; }
    static String tok() {
        while (pos < data.length && ws(data[pos])) pos++;
        int s = pos;
        while (pos < data.length && !ws(data[pos])) pos++;
        return new String(data, s, pos - s, StandardCharsets.UTF_8);
    }
    static int rdInt() { return Integer.parseInt(tok()); }
    static long rdLong() { return Long.parseLong(tok()); }
    static double rdFloat() { return Double.parseDouble(tok()); }
    static boolean rdBool() { return tok().equals("1"); }
    static String rdString() {
        int n = rdInt();
        if (pos < data.length && data[pos] == '\r') pos++;
        pos++;
        String s = new String(data, pos, n, StandardCharsets.UTF_8);
        pos += n;
        return s;
    }
    static int[] rdIntList() { int n = rdInt(); int[] a = new int[n]; for (int i = 0; i < n; i++) a[i] = rdInt(); return a; }
    static long[] rdLongList() { int n = rdInt(); long[] a = new long[n]; for (int i = 0; i < n; i++) a[i] = rdLong(); return a; }
    static double[] rdFloatList() { int n = rdInt(); double[] a = new double[n]; for (int i = 0; i < n; i++) a[i] = rdFloat(); return a; }
    static boolean[] rdBoolList() { int n = rdInt(); boolean[] a = new boolean[n]; for (int i = 0; i < n; i++) a[i] = rdBool(); return a; }
    static String[] rdStringList() { int n = rdInt(); String[] a = new String[n]; for (int i = 0; i < n; i++) a[i] = rdString(); return a; }
    static int[][] rdIntMatrix() { int n = rdInt(); int[][] a = new int[n][]; for (int i = 0; i < n; i++) a[i] = rdIntList(); return a; }
    static String[][] rdStringGrid() { int n = rdInt(); String[][] a = new String[n][]; for (int i = 0; i < n; i++) a[i] = rdStringList(); return a; }
    static Map<String, Integer> rdMapInt() { int n = rdInt(); Map<String, Integer> m = new LinkedHashMap<>(); for (int i = 0; i < n; i++) { String k = rdString(); m.put(k, rdInt()); } return m; }
    static Map<String, String> rdMapString() { int n = rdInt(); Map<String, String> m = new LinkedHashMap<>(); for (int i = 0; i < n; i++) { String k = rdString(); m.put(k, rdString()); } return m; }

    static ByteArrayOutputStream out = new ByteArrayOutputStream();
    static void raw(String s) { byte[] b = s.getBytes(StandardCharsets.UTF_8); out.write(b, 0, b.length); out.write('\n'); }
    static void wrInt(int v) { raw(Integer.toString(v)); }
    static void wrLong(long v) { raw(Long.toString(v)); }
    static void wrFloat(double v) { raw(Double.toString(v)); }
    static void wrBool(boolean v) { raw(v ? "1" : "0"); }
    static void wrString(String s) { byte[] b = s.getBytes(StandardCharsets.UTF_8); raw(Integer.toString(b.length)); out.write(b, 0, b.length); out.write('\n'); }
    static void wrIntList(int[] v) { wrInt(v.length); for (int x : v) wrInt(x); }
    static void wrLongList(long[] v) { wrInt(v.length); for (long x : v) wrLong(x); }
    static void wrFloatList(double[] v) { wrInt(v.length); for (double x : v) wrFloat(x); }
    static void wrBoolList(boolean[] v) { wrInt(v.length); for (boolean x : v) wrBool(x); }
    static void wrStringList(String[] v) { wrInt(v.length); for (String x : v) wrString(x); }
    static void wrIntMatrix(int[][] v) { wrInt(v.length); for (int[] x : v) wrIntList(x); }
    static void wrStringGrid(String[][] v) { wrInt(v.length); for (String[] x : v) wrStringList(x); }
    static void wrMapInt(Map<String, Integer> v) { wrInt(v.size()); for (Map.Entry<String, Integer> e : v.entrySet()) { wrString(e.getKey()); wrInt(e.getValue()); } }
    static void wrMapString(Map<String, String> v) { wrInt(v.size()); for (Map.Entry<String, String> e : v.entrySet()) { wrString(e.getKey()); wrString(e.getValue()); } }

    public static void main(String[] argv) throws Exception {
        data = System.in.readAllBytes();
%[1]s        var res = %[2]s;
        System.out.flush();
        %[3]s(res);
        OutputStream os = new FileOutputStream(FileDescriptor.out);
        os.write("\n@@RESULT@@\n".getBytes(StandardCharsets.UTF_8));
        out.writeTo(os);
        os.flush();
    }
}
`

func csharpDriver(sig Signature) string {
	decls, call := typedCall(sig,
		func(t string) string { return "Rd" + typedName(t) + "()" },
		func(n, e string) string { return "        var " + n + " = " + e + ";\n" })
	return fmt.Sprintf(csharpRuntime, decls, "Solution."+call, "Wr"+typedName(sig.Returns))
}

const csharpRuntime = `using System;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Text;

static class Program {
    static byte[] data;
    static int pos;

    static bool Ws(byte c) => c == 32 || c == 10 || c == 13 || c == 9;
    static string Tok() {
        while (pos < data.Length && Ws(data[pos])) pos++;
        int s = pos;
        while (pos < data.Length && !Ws(data[pos])) pos++;
        return Encoding.UTF8.GetString(data, s, pos - s);
    }
    static int RdInt() => int.Parse(Tok(), CultureInfo.InvariantCulture);
    static long RdLong() => long.Parse(Tok(), CultureInfo.InvariantCulture);
    static double RdFloat() => double.Parse(Tok(), NumberStyles.Float, CultureInfo.InvariantCulture);
    static bool RdBool() => Tok() == "1";
    static string RdString() {
        int n = RdInt();
        if (pos < data.Length && data[pos] == 13) pos++;
        pos++;
        string s = Encoding.UTF8.GetString(data, pos, n);
        pos += n;
        return s;
    }
    static int[] RdIntList() { int n = RdInt(); var a = new int[n]; for (int i = 0; i < n; i++) a[i] = RdInt(); return a; }
    static long[] RdLongList() { int n = RdInt(); var a = new long[n]; for (int i = 0; i < n; i++) a[i] = RdLong(); return a; }
    static double[] RdFloatList() { int n = RdInt(); var a = new double[n]; for (int i = 0; i < n; i++) a[i] = RdFloat(); return a; }
    static bool[] RdBoolList() { int n = RdInt(); var a = new bool[n]; for (int i = 0; i < n; i++) a[i] = RdBool(); return a; }
    static string[] RdStringList() { int n = RdInt(); var a = new string[n]; for (int i = 0; i < n; i++) a[i] = RdString(); return a; }
    static int[][] RdIntMatrix() { int n = RdInt(); var a = new int[n][]; for (int i = 0; i < n; i++) a[i] = RdIntList(); return a; }
    static string[][] RdStringGrid() { int n = RdInt(); var a = new string[n][]; for (int i = 0; i < n; i++) a[i] = RdStringList(); return a; }
    static Dictionary<string, int> RdMapInt() { int n = RdInt(); var m = new Dictionary<string, int>(); for (int i = 0; i < n; i++) { var k = RdString(); m[k] = RdInt(); } return m; }
    static Dictionary<string, string> RdMapString() { int n = RdInt(); var m = new Dictionary<string, string>(); for (int i = 0; i < n; i++) { var k = RdString(); m[k] = RdString(); } return m; }

    static MemoryStream outp = new MemoryStream();
    static void Raw(string s) { var b = Encoding.UTF8.GetBytes(s); outp.Write(b, 0, b.Length); outp.WriteByte(10); }
    static void WrInt(int v) => Raw(v.ToString(CultureInfo.InvariantCulture));
    static void WrLong(long v) => Raw(v.ToString(CultureInfo.InvariantCulture));
    static void WrFloat(double v) => Raw(v.ToString("R", CultureInfo.InvariantCulture));
    static void WrBool(bool v) => Raw(v ? "1" : "0");
    static void WrString(string s) { var b = Encoding.UTF8.GetBytes(s ?? ""); Raw(b.Length.ToString(CultureInfo.InvariantCulture)); outp.Write(b, 0, b.Length); outp.WriteByte(10); }
    static void WrIntList(int[] v) { WrInt(v.Length); foreach (var x in v) WrInt(x); }
    static void WrLongList(long[] v) { WrInt(v.Length); foreach (var x in v) WrLong(x); }
    static void WrFloatList(double[] v) { WrInt(v.Length); foreach (var x in v) WrFloat(x); }
    static void WrBoolList(bool[] v) { WrInt(v.Length); foreach (var x in v) WrBool(x); }
    static void WrStringList(string[] v) { WrInt(v.Length); foreach (var x in v) WrString(x); }
    static void WrIntMatrix(int[][] v) { WrInt(v.Length); foreach (var x in v) WrIntList(x); }
    static void WrStringGrid(string[][] v) { WrInt(v.Length); foreach (var x in v) WrStringList(x); }
    static void WrMapInt(Dictionary<string, int> v) { WrInt(v.Count); foreach (var e in v) { WrString(e.Key); WrInt(e.Value); } }
    static void WrMapString(Dictionary<string, string> v) { WrInt(v.Count); foreach (var e in v) { WrString(e.Key); WrString(e.Value); } }

    static void Main() {
        using (var stdin = Console.OpenStandardInput()) { var ms = new MemoryStream(); stdin.CopyTo(ms); data = ms.ToArray(); }
%[1]s        var res = %[2]s;
        Console.Out.Flush();
        %[3]s(res);
        using (var stdout = Console.OpenStandardOutput()) {
            var m = Encoding.UTF8.GetBytes("\n@@RESULT@@\n");
            stdout.Write(m, 0, m.Length);
            outp.WriteTo(stdout);
            stdout.Flush();
        }
    }
}
`

func cppDriver(sig Signature) string {
	decls, call := typedCall(sig,
		func(t string) string { return "harness::rd" + typedName(t) + "()" },
		func(n, e string) string { return "    auto " + n + " = " + e + ";\n" })
	return fmt.Sprintf(cppRuntime, decls, call, "harness::wr"+typedName(sig.Returns))
}

const cppRuntime = `#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <string>
#include <vector>
#include <map>
#include <unordered_map>
#include <algorithm>
#include <iostream>
#include <iterator>
#include "solution.cpp"

namespace harness {
static std::string data;
static size_t pos = 0;
static bool ws(char c) { return c == ' ' || c == '\n' || c == '\r' || c == '\t'; }
static std::string tok() {
    while (pos < data.size() && ws(data[pos])) pos++;
    size_t s = pos;
    while (pos < data.size() && !ws(data[pos])) pos++;
    return data.substr(s, pos - s);
}
static int rdInt() { return std::stoi(tok()); }
static long long rdLong() { return std::stoll(tok()); }
static double rdFloat() { return std::strtod(tok().c_str(), nullptr); }
static bool rdBool() { return tok() == "1"; }
static std::string rdString() {
    int n = rdInt();
    if (pos < data.size() && data[pos] == '\r') pos++;
    pos++;
    std::string s = data.substr(pos, n);
    pos += n;
    return s;
}
static std::vector<int> rdIntList() { int n = rdInt(); std::vector<int> v; v.reserve(n); for (int i = 0; i < n; i++) v.push_back(rdInt()); return v; }
static std::vector<long long> rdLongList() { int n = rdInt(); std::vector<long long> v; v.reserve(n); for (int i = 0; i < n; i++) v.push_back(rdLong()); return v; }
static std::vector<double> rdFloatList() { int n = rdInt(); std::vector<double> v; v.reserve(n); for (int i = 0; i < n; i++) v.push_back(rdFloat()); return v; }
static std::vector<bool> rdBoolList() { int n = rdInt(); std::vector<bool> v; v.reserve(n); for (int i = 0; i < n; i++) v.push_back(rdBool()); return v; }
static std::vector<std::string> rdStringList() { int n = rdInt(); std::vector<std::string> v; v.reserve(n); for (int i = 0; i < n; i++) v.push_back(rdString()); return v; }
static std::vector<std::vector<int>> rdIntMatrix() { int n = rdInt(); std::vector<std::vector<int>> v; v.reserve(n); for (int i = 0; i < n; i++) v.push_back(rdIntList()); return v; }
static std::vector<std::vector<std::string>> rdStringGrid() { int n = rdInt(); std::vector<std::vector<std::string>> v; v.reserve(n); for (int i = 0; i < n; i++) v.push_back(rdStringList()); return v; }
static std::map<std::string, int> rdMapInt() { int n = rdInt(); std::map<std::string, int> m; for (int i = 0; i < n; i++) { std::string k = rdString(); m[k] = rdInt(); } return m; }
static std::map<std::string, std::string> rdMapString() { int n = rdInt(); std::map<std::string, std::string> m; for (int i = 0; i < n; i++) { std::string k = rdString(); m[k] = rdString(); } return m; }

static std::string out;
static void raw(const std::string& s) { out += s; out += '\n'; }
static void wrInt(long long v) { raw(std::to_string(v)); }
static void wrLong(long long v) { raw(std::to_string(v)); }
static void wrFloat(double v) { char buf[64]; std::snprintf(buf, sizeof buf, "%%.17g", v); raw(buf); }
static void wrBool(bool v) { raw(v ? "1" : "0"); }
static void wrString(const std::string& s) { raw(std::to_string(s.size())); out += s; out += '\n'; }
static void wrIntList(const std::vector<int>& v) { wrInt((long long)v.size()); for (int x : v) wrInt(x); }
static void wrLongList(const std::vector<long long>& v) { wrInt((long long)v.size()); for (long long x : v) wrLong(x); }
static void wrFloatList(const std::vector<double>& v) { wrInt((long long)v.size()); for (double x : v) wrFloat(x); }
static void wrBoolList(const std::vector<bool>& v) { wrInt((long long)v.size()); for (bool x : v) wrBool(x); }
static void wrStringList(const std::vector<std::string>& v) { wrInt((long long)v.size()); for (const auto& x : v) wrString(x); }
static void wrIntMatrix(const std::vector<std::vector<int>>& v) { wrInt((long long)v.size()); for (const auto& x : v) wrIntList(x); }
static void wrStringGrid(const std::vector<std::vector<std::string>>& v) { wrInt((long long)v.size()); for (const auto& x : v) wrStringList(x); }
static void wrMapInt(const std::map<std::string, int>& v) { wrInt((long long)v.size()); for (const auto& e : v) { wrString(e.first); wrInt(e.second); } }
static void wrMapString(const std::map<std::string, std::string>& v) { wrInt((long long)v.size()); for (const auto& e : v) { wrString(e.first); wrString(e.second); } }
}

int main() {
    { std::istreambuf_iterator<char> b(std::cin), e; harness::data.assign(b, e); }
%[1]s    auto res = %[2]s;
    std::fflush(stdout);
    std::cout.flush();
    %[3]s(res);
    std::fwrite("\n@@RESULT@@\n", 1, 12, stdout);
    std::fwrite(harness::out.data(), 1, harness::out.size(), stdout);
    std::fflush(stdout);
    return 0;
}
`

func cDriver(sig Signature) string {
	var decls strings.Builder
	call := make([]string, 0, len(sig.Params)*3)
	for i, p := range sig.Params {
		n := fmt.Sprintf("a%d", i)
		switch p.Type {
		case TypeInt:
			fmt.Fprintf(&decls, "    int %s = h_int();\n", n)
			call = append(call, n)
		case TypeLong:
			fmt.Fprintf(&decls, "    long long %s = h_long();\n", n)
			call = append(call, n)
		case TypeFloat:
			fmt.Fprintf(&decls, "    double %s = h_float();\n", n)
			call = append(call, n)
		case TypeBool:
			fmt.Fprintf(&decls, "    bool %s = h_bool();\n", n)
			call = append(call, n)
		case TypeString:
			fmt.Fprintf(&decls, "    char *%s = h_string();\n", n)
			call = append(call, n)
		case TypeIntList:
			fmt.Fprintf(&decls, "    int %s_len; int *%s = h_int_list(&%s_len);\n", n, n, n)
			call = append(call, n, n+"_len")
		case TypeLongList:
			fmt.Fprintf(&decls, "    int %s_len; long long *%s = h_long_list(&%s_len);\n", n, n, n)
			call = append(call, n, n+"_len")
		case TypeFloatList:
			fmt.Fprintf(&decls, "    int %s_len; double *%s = h_float_list(&%s_len);\n", n, n, n)
			call = append(call, n, n+"_len")
		case TypeBoolList:
			fmt.Fprintf(&decls, "    int %s_len; bool *%s = h_bool_list(&%s_len);\n", n, n, n)
			call = append(call, n, n+"_len")
		case TypeStringList:
			fmt.Fprintf(&decls, "    int %s_len; char **%s = h_string_list(&%s_len);\n", n, n, n)
			call = append(call, "(const char **)"+n, n+"_len")
		case TypeIntMatrix:
			fmt.Fprintf(&decls, "    int %s_len; int *%s_lens; int **%s = h_int_matrix(&%s_len, &%s_lens);\n", n, n, n, n, n)
			call = append(call, "(const int **)"+n, n+"_len", n+"_lens")
		case TypeStringGrid:
			fmt.Fprintf(&decls, "    int %s_len; int *%s_lens; char ***%s = h_string_grid(&%s_len, &%s_lens);\n", n, n, n, n, n)
			call = append(call, "(const char ***)"+n, n+"_len", n+"_lens")
		case TypeMapInt:
			fmt.Fprintf(&decls, "    char **%s_keys; int *%s_vals; int %s_len = h_map_int(&%s_keys, &%s_vals);\n", n, n, n, n, n)
			call = append(call, "(const char **)"+n+"_keys", n+"_vals", n+"_len")
		case TypeMapString:
			fmt.Fprintf(&decls, "    char **%s_keys; char **%s_vals; int %s_len = h_map_string(&%s_keys, &%s_vals);\n", n, n, n, n, n)
			call = append(call, "(const char **)"+n+"_keys", "(const char **)"+n+"_vals", n+"_len")
		}
	}
	var ret, write string
	switch sig.Returns {
	case TypeInt:
		ret, write = "int res = %s;", "h_wr_int(res);"
	case TypeLong:
		ret, write = "long long res = %s;", "h_wr_int(res);"
	case TypeFloat:
		ret, write = "double res = %s;", "h_wr_float(res);"
	case TypeBool:
		ret, write = "bool res = %s;", "h_wr_bool(res);"
	case TypeString:
		ret, write = "char *res = %s;", "h_wr_string(res);"
	case TypeIntList:
		call = append(call, "&out_len")
		ret, write = "int out_len = 0; int *res = %s;", "h_wr_int_list(res, out_len);"
	case TypeLongList:
		call = append(call, "&out_len")
		ret, write = "int out_len = 0; long long *res = %s;", "h_wr_long_list(res, out_len);"
	case TypeFloatList:
		call = append(call, "&out_len")
		ret, write = "int out_len = 0; double *res = %s;", "h_wr_float_list(res, out_len);"
	case TypeBoolList:
		call = append(call, "&out_len")
		ret, write = "int out_len = 0; bool *res = %s;", "h_wr_bool_list(res, out_len);"
	case TypeStringList:
		call = append(call, "&out_len")
		ret, write = "int out_len = 0; char **res = %s;", "h_wr_string_list(res, out_len);"
	}
	invoke := sig.Name + "(" + strings.Join(call, ", ") + ")"
	return fmt.Sprintf(cRuntime, decls.String(), fmt.Sprintf(ret, invoke), write)
}

const cRuntime = `#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <stdbool.h>
#include <math.h>
#include "solution.c"

static char *h_data;
static size_t h_len, h_pos;
static char h_tokbuf[128];

static int h_ws(char c) { return c == ' ' || c == '\n' || c == '\r' || c == '\t'; }
static const char *h_tok(void) {
    while (h_pos < h_len && h_ws(h_data[h_pos])) h_pos++;
    size_t s = h_pos;
    while (h_pos < h_len && !h_ws(h_data[h_pos])) h_pos++;
    size_t n = h_pos - s;
    if (n >= sizeof h_tokbuf) n = sizeof h_tokbuf - 1;
    memcpy(h_tokbuf, h_data + s, n);
    h_tokbuf[n] = 0;
    return h_tokbuf;
}
static int h_int(void) { return (int)strtol(h_tok(), NULL, 10); }
static long long h_long(void) { return strtoll(h_tok(), NULL, 10); }
static double h_float(void) { return strtod(h_tok(), NULL); }
static bool h_bool(void) { return strcmp(h_tok(), "1") == 0; }
static char *h_string(void) {
    int n = h_int();
    if (h_pos < h_len && h_data[h_pos] == '\r') h_pos++;
    h_pos++;
    char *s = malloc((size_t)n + 1);
    memcpy(s, h_data + h_pos, (size_t)n);
    s[n] = 0;
    h_pos += (size_t)n;
    return s;
}
static int *h_int_list(int *len) { int n = h_int(); int *a = malloc(sizeof(int) * (size_t)(n > 0 ? n : 1)); for (int i = 0; i < n; i++) a[i] = h_int(); *len = n; return a; }
static long long *h_long_list(int *len) { int n = h_int(); long long *a = malloc(sizeof(long long) * (size_t)(n > 0 ? n : 1)); for (int i = 0; i < n; i++) a[i] = h_long(); *len = n; return a; }
static double *h_float_list(int *len) { int n = h_int(); double *a = malloc(sizeof(double) * (size_t)(n > 0 ? n : 1)); for (int i = 0; i < n; i++) a[i] = h_float(); *len = n; return a; }
static bool *h_bool_list(int *len) { int n = h_int(); bool *a = malloc(sizeof(bool) * (size_t)(n > 0 ? n : 1)); for (int i = 0; i < n; i++) a[i] = h_bool(); *len = n; return a; }
static char **h_string_list(int *len) { int n = h_int(); char **a = malloc(sizeof(char *) * (size_t)(n > 0 ? n : 1)); for (int i = 0; i < n; i++) a[i] = h_string(); *len = n; return a; }
static int **h_int_matrix(int *len, int **lens) { int n = h_int(); int **a = malloc(sizeof(int *) * (size_t)(n > 0 ? n : 1)); int *l = malloc(sizeof(int) * (size_t)(n > 0 ? n : 1)); for (int i = 0; i < n; i++) a[i] = h_int_list(&l[i]); *len = n; *lens = l; return a; }
static char ***h_string_grid(int *len, int **lens) { int n = h_int(); char ***a = malloc(sizeof(char **) * (size_t)(n > 0 ? n : 1)); int *l = malloc(sizeof(int) * (size_t)(n > 0 ? n : 1)); for (int i = 0; i < n; i++) a[i] = h_string_list(&l[i]); *len = n; *lens = l; return a; }
static int h_map_int(char ***keys, int **vals) { int n = h_int(); char **k = malloc(sizeof(char *) * (size_t)(n > 0 ? n : 1)); int *v = malloc(sizeof(int) * (size_t)(n > 0 ? n : 1)); for (int i = 0; i < n; i++) { k[i] = h_string(); v[i] = h_int(); } *keys = k; *vals = v; return n; }
static int h_map_string(char ***keys, char ***vals) { int n = h_int(); char **k = malloc(sizeof(char *) * (size_t)(n > 0 ? n : 1)); char **v = malloc(sizeof(char *) * (size_t)(n > 0 ? n : 1)); for (int i = 0; i < n; i++) { k[i] = h_string(); v[i] = h_string(); } *keys = k; *vals = v; return n; }

static char *h_out;
static size_t h_out_len, h_out_cap;
static void h_put(const char *s, size_t n) {
    if (h_out_len + n + 1 > h_out_cap) { h_out_cap = (h_out_cap + n + 1) * 2; h_out = realloc(h_out, h_out_cap); }
    memcpy(h_out + h_out_len, s, n);
    h_out_len += n;
}
static void h_raw(const char *s) { h_put(s, strlen(s)); h_put("\n", 1); }
static void h_wr_int(long long v) { char b[32]; snprintf(b, sizeof b, "%%lld", v); h_raw(b); }
static void h_wr_float(double v) { char b[64]; snprintf(b, sizeof b, "%%.17g", v); h_raw(b); }
static void h_wr_bool(bool v) { h_raw(v ? "1" : "0"); }
static void h_wr_string(const char *s) { if (!s) s = ""; char b[32]; snprintf(b, sizeof b, "%%zu", strlen(s)); h_raw(b); h_put(s, strlen(s)); h_put("\n", 1); }
static void h_wr_int_list(const int *a, int n) { h_wr_int(n); for (int i = 0; i < n; i++) h_wr_int(a[i]); }
static void h_wr_long_list(const long long *a, int n) { h_wr_int(n); for (int i = 0; i < n; i++) h_wr_int(a[i]); }
static void h_wr_float_list(const double *a, int n) { h_wr_int(n); for (int i = 0; i < n; i++) h_wr_float(a[i]); }
static void h_wr_bool_list(const bool *a, int n) { h_wr_int(n); for (int i = 0; i < n; i++) h_wr_bool(a[i]); }
static void h_wr_string_list(char **a, int n) { h_wr_int(n); for (int i = 0; i < n; i++) h_wr_string(a[i]); }

int main(void) {
    size_t cap = 1 << 16;
    h_data = malloc(cap);
    for (;;) {
        if (h_len == cap) { cap *= 2; h_data = realloc(h_data, cap); }
        size_t n = fread(h_data + h_len, 1, cap - h_len, stdin);
        if (n == 0) break;
        h_len += n;
    }
%[1]s    %[2]s
    fflush(stdout);
    %[3]s
    fwrite("\n@@RESULT@@\n", 1, 12, stdout);
    fwrite(h_out, 1, h_out_len, stdout);
    fflush(stdout);
    return 0;
}
`

func rustDriver(sig Signature) string {
	decls, call := typedCall(sig,
		func(t string) string { return "r." + rustName(t) + "()" },
		func(n, e string) string { return "    let " + n + " = " + e + ";\n" })
	return fmt.Sprintf(rustRuntime, decls, call, "w."+rustName(sig.Returns))
}

func rustName(typ string) string {
	switch typ {
	case TypeInt:
		return "int"
	case TypeLong:
		return "long"
	case TypeFloat:
		return "float"
	case TypeBool:
		return "boolean"
	case TypeString:
		return "string"
	case TypeIntList:
		return "int_list"
	case TypeLongList:
		return "long_list"
	case TypeFloatList:
		return "float_list"
	case TypeBoolList:
		return "bool_list"
	case TypeStringList:
		return "string_list"
	case TypeIntMatrix:
		return "int_matrix"
	case TypeStringGrid:
		return "string_grid"
	case TypeMapInt:
		return "map_int"
	case TypeMapString:
		return "map_string"
	}
	return ""
}

const rustRuntime = `#![allow(unused_imports, dead_code, unused_variables, unused_mut, non_snake_case, unused_parens)]
include!("solution.rs");

mod harness {
    pub struct R { pub data: Vec<u8>, pub pos: usize }
    impl R {
        fn ws(c: u8) -> bool { c == b' ' || c == b'\n' || c == b'\r' || c == b'\t' }
        pub fn tok(&mut self) -> String {
            while self.pos < self.data.len() && Self::ws(self.data[self.pos]) { self.pos += 1; }
            let s = self.pos;
            while self.pos < self.data.len() && !Self::ws(self.data[self.pos]) { self.pos += 1; }
            String::from_utf8_lossy(&self.data[s..self.pos]).into_owned()
        }
        pub fn int(&mut self) -> i32 { self.tok().parse().unwrap_or(0) }
        pub fn long(&mut self) -> i64 { self.tok().parse().unwrap_or(0) }
        pub fn float(&mut self) -> f64 { self.tok().parse().unwrap_or(0.0) }
        pub fn boolean(&mut self) -> bool { self.tok() == "1" }
        pub fn string(&mut self) -> String {
            let n = self.int() as usize;
            if self.pos < self.data.len() && self.data[self.pos] == b'\r' { self.pos += 1; }
            self.pos += 1;
            let s = String::from_utf8_lossy(&self.data[self.pos..self.pos + n]).into_owned();
            self.pos += n;
            s
        }
        pub fn int_list(&mut self) -> Vec<i32> { let n = self.int(); (0..n).map(|_| self.int()).collect() }
        pub fn long_list(&mut self) -> Vec<i64> { let n = self.int(); (0..n).map(|_| self.long()).collect() }
        pub fn float_list(&mut self) -> Vec<f64> { let n = self.int(); (0..n).map(|_| self.float()).collect() }
        pub fn bool_list(&mut self) -> Vec<bool> { let n = self.int(); (0..n).map(|_| self.boolean()).collect() }
        pub fn string_list(&mut self) -> Vec<String> { let n = self.int(); (0..n).map(|_| self.string()).collect() }
        pub fn int_matrix(&mut self) -> Vec<Vec<i32>> { let n = self.int(); (0..n).map(|_| self.int_list()).collect() }
        pub fn string_grid(&mut self) -> Vec<Vec<String>> { let n = self.int(); (0..n).map(|_| self.string_list()).collect() }
        pub fn map_int(&mut self) -> std::collections::HashMap<String, i32> { let n = self.int(); let mut m = std::collections::HashMap::new(); for _ in 0..n { let k = self.string(); let v = self.int(); m.insert(k, v); } m }
        pub fn map_string(&mut self) -> std::collections::HashMap<String, String> { let n = self.int(); let mut m = std::collections::HashMap::new(); for _ in 0..n { let k = self.string(); let v = self.string(); m.insert(k, v); } m }
    }
    pub struct W { pub out: Vec<u8> }
    impl W {
        pub fn raw(&mut self, s: &str) { self.out.extend_from_slice(s.as_bytes()); self.out.push(b'\n'); }
        pub fn int(&mut self, v: i32) { self.raw(&v.to_string()); }
        pub fn long(&mut self, v: i64) { self.raw(&v.to_string()); }
        pub fn float(&mut self, v: f64) { self.raw(&format!("{:?}", v)); }
        pub fn boolean(&mut self, v: bool) { self.raw(if v { "1" } else { "0" }); }
        pub fn string(&mut self, s: String) { self.raw(&s.len().to_string()); self.out.extend_from_slice(s.as_bytes()); self.out.push(b'\n'); }
        pub fn int_list(&mut self, v: Vec<i32>) { self.int(v.len() as i32); for x in v { self.int(x); } }
        pub fn long_list(&mut self, v: Vec<i64>) { self.int(v.len() as i32); for x in v { self.long(x); } }
        pub fn float_list(&mut self, v: Vec<f64>) { self.int(v.len() as i32); for x in v { self.float(x); } }
        pub fn bool_list(&mut self, v: Vec<bool>) { self.int(v.len() as i32); for x in v { self.boolean(x); } }
        pub fn string_list(&mut self, v: Vec<String>) { self.int(v.len() as i32); for x in v { self.string(x); } }
        pub fn int_matrix(&mut self, v: Vec<Vec<i32>>) { self.int(v.len() as i32); for x in v { self.int_list(x); } }
        pub fn string_grid(&mut self, v: Vec<Vec<String>>) { self.int(v.len() as i32); for x in v { self.string_list(x); } }
        pub fn map_int(&mut self, v: std::collections::HashMap<String, i32>) { self.int(v.len() as i32); for (k, x) in v { self.string(k); self.int(x); } }
        pub fn map_string(&mut self, v: std::collections::HashMap<String, String>) { self.int(v.len() as i32); for (k, x) in v { self.string(k); self.string(x); } }
    }
}

fn main() {
    use std::io::{Read, Write};
    let mut r = harness::R { data: Vec::new(), pos: 0 };
    std::io::stdin().read_to_end(&mut r.data).unwrap();
%[1]s    let res = %[2]s;
    std::io::stdout().flush().unwrap();
    let mut w = harness::W { out: Vec::new() };
    %[3]s(res);
    let mut so = std::io::stdout();
    so.write_all(b"\n@@RESULT@@\n").unwrap();
    so.write_all(&w.out).unwrap();
    so.flush().unwrap();
}
`
