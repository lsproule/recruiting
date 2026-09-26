//go:build integration

package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"recruiting/runner/wire"
)

// Three signatures that between them touch every value type: a mixed call
// returning a string list, a float reduction, and a map return. Every
// language gets a solution for each, so a driver that mis-reads or
// mis-writes any type fails here rather than in a candidate's sitting.

var mixedSig = wire.Signature{
	Name: "solve",
	Params: []wire.Param{
		{Name: "nums", Type: wire.TypeIntList}, {Name: "label", Type: wire.TypeString},
		{Name: "counts", Type: wire.TypeMapInt}, {Name: "grid", Type: wire.TypeIntMatrix},
		{Name: "flag", Type: wire.TypeBool}, {Name: "big", Type: wire.TypeLong},
	},
	Returns: wire.TypeStringList,
}

var avgSig = wire.Signature{
	Name:    "average",
	Params:  []wire.Param{{Name: "values", Type: wire.TypeFloatList}},
	Returns: wire.TypeFloat,
}

var tallySig = wire.Signature{
	Name:    "tally",
	Params:  []wire.Param{{Name: "words", Type: wire.TypeStringList}},
	Returns: wire.TypeMapInt,
}

// mixed: [label + ":" + sum(nums), sum of counts values, cells in grid, yes/no, big*2]
var mixedCases = []Test{
	{ID: uuid.NewString(), Input: `[[1,2,3], "héllo wörld", {"a": 1, "b": 41}, [[1,2],[3]], true, 5000000000]`, Expected: `["héllo wörld:6","42","3","yes","10000000000"]`, Weight: 1},
	{ID: uuid.NewString(), Input: `[[], "x y", {}, [], false, -1]`, Expected: `["x y:0","0","0","no","-2"]`, Weight: 1},
	{ID: uuid.NewString(), Input: `[[1], "z", {}, [], false, 0]`, Expected: `["wrong"]`, Weight: 1},
}

var avgCases = []Test{
	{ID: uuid.NewString(), Input: `[[0.1, 0.2, 0.3]]`, Expected: `0.2`, Weight: 1},
	{ID: uuid.NewString(), Input: `[[1e20, 1e20]]`, Expected: `1e+20`, Weight: 1},
	{ID: uuid.NewString(), Input: `[[]]`, Expected: `0`, Weight: 1},
}

var tallyCases = []Test{
	{ID: uuid.NewString(), Input: `[["a", "b", "a", "ünï"]]`, Expected: `{"a": 2, "b": 1, "ünï": 1}`, Weight: 1},
	{ID: uuid.NewString(), Input: `[[]]`, Expected: `{}`, Weight: 1},
}

var functionSolutions = map[string]struct{ mixed, avg, tally string }{
	"python": {
		mixed: "def solve(nums, label, counts, grid, flag, big):\n    print('debug')\n    return [label + ':' + str(sum(nums)), str(sum(counts.values())), str(sum(len(r) for r in grid)), 'yes' if flag else 'no', str(big * 2)]\n",
		avg:   "def average(values):\n    return sum(values) / len(values) if values else 0.0\n",
		tally: "def tally(words):\n    d = {}\n    for w in words:\n        d[w] = d.get(w, 0) + 1\n    return d\n",
	},
	"javascript": {
		mixed: "function solve(nums, label, counts, grid, flag, big) {\n  console.log('debug');\n  const s = nums.reduce((a, b) => a + b, 0);\n  const c = Object.values(counts).reduce((a, b) => a + b, 0);\n  const cells = grid.reduce((a, r) => a + r.length, 0);\n  return [label + ':' + s, String(c), String(cells), flag ? 'yes' : 'no', String(big * 2)];\n}\n",
		avg:   "const average = (values) => values.length ? values.reduce((a, b) => a + b, 0) / values.length : 0;\n",
		tally: "function tally(words) { const m = {}; for (const w of words) m[w] = (m[w] || 0) + 1; return m; }\n",
	},
	"ruby": {
		mixed: "def solve(nums, label, counts, grid, flag, big)\n  puts 'debug'\n  [\"#{label}:#{nums.sum}\", counts.values.sum.to_s, grid.sum { |r| r.length }.to_s, flag ? 'yes' : 'no', (big * 2).to_s]\nend\n",
		avg:   "def average(values)\n  values.empty? ? 0.0 : values.sum / values.length\nend\n",
		tally: "def tally(words)\n  words.tally\nend\n",
	},
	"php": {
		mixed: "<?php\nfunction solve(array $nums, string $label, array $counts, array $grid, bool $flag, int $big): array {\n    echo \"debug\\n\";\n    $cells = 0; foreach ($grid as $r) $cells += count($r);\n    return [$label . ':' . array_sum($nums), strval(array_sum($counts)), strval($cells), $flag ? 'yes' : 'no', strval($big * 2)];\n}\n",
		avg:   "<?php\nfunction average(array $values): float { return count($values) ? array_sum($values) / count($values) : 0.0; }\n",
		tally: "<?php\nfunction tally(array $words): array { $m = []; foreach ($words as $w) { $m[$w] = ($m[$w] ?? 0) + 1; } return $m; }\n",
	},
	"go": {
		mixed: "package main\n\nimport (\n\t\"fmt\"\n\t\"strconv\"\n)\n\nfunc solve(nums []int, label string, counts map[string]int, grid [][]int, flag bool, big int64) []string {\n\tfmt.Println(\"debug\")\n\ts := 0\n\tfor _, n := range nums {\n\t\ts += n\n\t}\n\tc := 0\n\tfor _, v := range counts {\n\t\tc += v\n\t}\n\tcells := 0\n\tfor _, r := range grid {\n\t\tcells += len(r)\n\t}\n\tyes := \"no\"\n\tif flag {\n\t\tyes = \"yes\"\n\t}\n\treturn []string{label + \":\" + strconv.Itoa(s), strconv.Itoa(c), strconv.Itoa(cells), yes, strconv.FormatInt(big*2, 10)}\n}\n",
		avg:   "package main\n\nfunc average(values []float64) float64 {\n\tif len(values) == 0 {\n\t\treturn 0\n\t}\n\ts := 0.0\n\tfor _, v := range values {\n\t\ts += v\n\t}\n\treturn s / float64(len(values))\n}\n",
		tally: "package main\n\nfunc tally(words []string) map[string]int {\n\tm := map[string]int{}\n\tfor _, w := range words {\n\t\tm[w]++\n\t}\n\treturn m\n}\n",
	},
	"java": {
		mixed: "import java.util.*;\n\nclass Solution {\n    public static String[] solve(int[] nums, String label, Map<String, Integer> counts, int[][] grid, boolean flag, long big) {\n        System.out.println(\"debug\");\n        int s = 0; for (int n : nums) s += n;\n        int c = 0; for (int v : counts.values()) c += v;\n        int cells = 0; for (int[] r : grid) cells += r.length;\n        return new String[]{label + \":\" + s, String.valueOf(c), String.valueOf(cells), flag ? \"yes\" : \"no\", String.valueOf(big * 2)};\n    }\n}\n",
		avg:   "class Solution {\n    public static double average(double[] values) {\n        if (values.length == 0) return 0;\n        double s = 0; for (double v : values) s += v;\n        return s / values.length;\n    }\n}\n",
		tally: "import java.util.*;\n\nclass Solution {\n    public static Map<String, Integer> tally(String[] words) {\n        Map<String, Integer> m = new HashMap<>();\n        for (String w : words) m.merge(w, 1, Integer::sum);\n        return m;\n    }\n}\n",
	},
	"csharp": {
		mixed: "using System;\nusing System.Collections.Generic;\nusing System.Linq;\n\nclass Solution {\n    public static string[] solve(int[] nums, string label, Dictionary<string, int> counts, int[][] grid, bool flag, long big) {\n        Console.WriteLine(\"debug\");\n        return new[] { label + \":\" + nums.Sum(), counts.Values.Sum().ToString(), grid.Sum(r => r.Length).ToString(), flag ? \"yes\" : \"no\", (big * 2).ToString() };\n    }\n}\n",
		avg:   "using System.Linq;\n\nclass Solution {\n    public static double average(double[] values) => values.Length == 0 ? 0 : values.Average();\n}\n",
		tally: "using System.Collections.Generic;\n\nclass Solution {\n    public static Dictionary<string, int> tally(string[] words) {\n        var m = new Dictionary<string, int>();\n        foreach (var w in words) m[w] = m.GetValueOrDefault(w) + 1;\n        return m;\n    }\n}\n",
	},
	"cpp": {
		mixed: "#include <string>\n#include <vector>\n#include <map>\n#include <iostream>\nusing namespace std;\n\nvector<string> solve(vector<int> nums, string label, map<string, int> counts, vector<vector<int>> grid, bool flag, long long big) {\n    cout << \"debug\" << endl;\n    long long s = 0; for (int n : nums) s += n;\n    long long c = 0; for (auto& e : counts) c += e.second;\n    size_t cells = 0; for (auto& r : grid) cells += r.size();\n    return {label + \":\" + to_string(s), to_string(c), to_string(cells), flag ? \"yes\" : \"no\", to_string(big * 2)};\n}\n",
		avg:   "#include <vector>\n\ndouble average(std::vector<double> values) {\n    if (values.empty()) return 0;\n    double s = 0; for (double v : values) s += v;\n    return s / values.size();\n}\n",
		tally: "#include <map>\n#include <string>\n#include <vector>\n\nstd::map<std::string, int> tally(std::vector<std::string> words) {\n    std::map<std::string, int> m;\n    for (auto& w : words) m[w]++;\n    return m;\n}\n",
	},
	"c": {
		mixed: "#include <stdio.h>\n#include <stdlib.h>\n#include <string.h>\n#include <stdbool.h>\n\nchar **solve(const int *nums, int nums_len, const char *label, const char **counts_keys, const int *counts_values, int counts_len, const int **grid, int grid_len, const int *grid_lens, bool flag, long long big, int *out_len) {\n    printf(\"debug\\n\");\n    long long s = 0; for (int i = 0; i < nums_len; i++) s += nums[i];\n    long long c = 0; for (int i = 0; i < counts_len; i++) c += counts_values[i];\n    int cells = 0; for (int i = 0; i < grid_len; i++) cells += grid_lens[i];\n    char **out = malloc(sizeof(char *) * 5);\n    out[0] = malloc(strlen(label) + 32); sprintf(out[0], \"%s:%lld\", label, s);\n    out[1] = malloc(32); sprintf(out[1], \"%lld\", c);\n    out[2] = malloc(32); sprintf(out[2], \"%d\", cells);\n    out[3] = flag ? \"yes\" : \"no\";\n    out[4] = malloc(32); sprintf(out[4], \"%lld\", big * 2);\n    *out_len = 5;\n    return out;\n}\n",
		avg:   "double average(const double *values, int values_len) {\n    if (values_len == 0) return 0;\n    double s = 0; for (int i = 0; i < values_len; i++) s += values[i];\n    return s / values_len;\n}\n",
	},
	"rust": {
		mixed: "use std::collections::HashMap;\n\nfn solve(nums: Vec<i32>, label: String, counts: HashMap<String, i32>, grid: Vec<Vec<i32>>, flag: bool, big: i64) -> Vec<String> {\n    println!(\"debug\");\n    let s: i32 = nums.iter().sum();\n    let c: i32 = counts.values().sum();\n    let cells: usize = grid.iter().map(|r| r.len()).sum();\n    vec![format!(\"{}:{}\", label, s), c.to_string(), cells.to_string(), if flag { \"yes\".to_string() } else { \"no\".to_string() }, (big * 2).to_string()]\n}\n",
		avg:   "fn average(values: Vec<f64>) -> f64 {\n    if values.is_empty() { return 0.0; }\n    values.iter().sum::<f64>() / values.len() as f64\n}\n",
		tally: "use std::collections::HashMap;\n\nfn tally(words: Vec<String>) -> HashMap<String, i32> {\n    let mut m = HashMap::new();\n    for w in words { *m.entry(w).or_insert(0) += 1; }\n    m\n}\n",
	},
}

func runFunction(t *testing.T, d Executor, lang, source string, sig wire.Signature, tests []Test) *Response {
	t.Helper()
	req := &Request{ID: uuid.NewString(), Language: lang, Source: source, Signature: &sig, Tests: tests, Limits: DefaultLimits}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	return d.Execute(ctx, req)
}

func TestFunctionProblemsInEveryLanguage(t *testing.T) {
	d := dockerExecutor(t)
	for _, lang := range wire.Languages {
		sol, ok := functionSolutions[lang]
		if !ok {
			t.Errorf("no solutions for %s", lang)
			continue
		}
		t.Run(lang, func(t *testing.T) {
			requireImage(t, d, lang)
			resp := runFunction(t, d, lang, sol.mixed, mixedSig, mixedCases)
			if resp.Status != StatusOK {
				t.Fatalf("mixed status %s: compile=%q results=%+v", resp.Status, resp.CompileOutput, resp.Results)
			}
			if len(resp.Results) != 3 || resp.Results[0].Status != TestPass || resp.Results[1].Status != TestPass || resp.Results[2].Status != TestFail {
				t.Fatalf("mixed results %+v", resp.Results)
			}
			if resp.Results[0].StdoutTail != `["héllo wörld:6","42","3","yes","10000000000"]` {
				t.Errorf("mixed result text = %q", resp.Results[0].StdoutTail)
			}
			if !contains(resp.Results[0].StderrTail, "debug") {
				t.Errorf("the candidate's print was lost: stderr=%q", resp.Results[0].StderrTail)
			}
			resp = runFunction(t, d, lang, sol.avg, avgSig, avgCases)
			if resp.Status != StatusOK || len(resp.Results) != 3 {
				t.Fatalf("avg status %s: compile=%q results=%+v", resp.Status, resp.CompileOutput, resp.Results)
			}
			for i, r := range resp.Results {
				if r.Status != TestPass {
					t.Errorf("avg case %d: %+v", i, r)
				}
			}
			if sol.tally == "" {
				return
			}
			resp = runFunction(t, d, lang, sol.tally, tallySig, tallyCases)
			if resp.Status != StatusOK || len(resp.Results) != 2 {
				t.Fatalf("tally status %s: compile=%q results=%+v", resp.Status, resp.CompileOutput, resp.Results)
			}
			for i, r := range resp.Results {
				if r.Status != TestPass {
					t.Errorf("tally case %d: %+v", i, r)
				}
			}
			if resp.Results[0].StdoutTail != `{"a":2,"b":1,"ünï":1}` {
				t.Errorf("tally result text = %q", resp.Results[0].StdoutTail)
			}
		})
	}
}

// A function that never returns, or returns the wrong shape, is an error
// with a reason rather than a silent fail.
func TestFunctionProblemFaultsAreReported(t *testing.T) {
	d := dockerExecutor(t)
	requireImage(t, d, "python")
	resp := runFunction(t, d, "python", "def average(values):\n    raise ValueError('nope')\n", avgSig, avgCases[:1])
	if resp.Status != StatusOK || resp.Results[0].Status != TestError || !contains(resp.Results[0].StderrTail, "nope") {
		t.Fatalf("raise: %+v", resp)
	}
	resp = runFunction(t, d, "python", "def average(values):\n    return 'a string'\n", avgSig, avgCases[:1])
	// Python's own driver refuses to spell a string as a float before the
	// harness ever sees the result; either way the case is an error with a
	// reason, never a pass.
	if resp.Results[0].Status != TestError || resp.Results[0].StderrTail == "" {
		t.Fatalf("wrong shape: %+v", resp.Results[0])
	}
	resp = runFunction(t, d, "python", "def average(values):\n    while True: pass\n", avgSig, avgCases[:1])
	if resp.Results[0].Status != TestTimeout {
		t.Fatalf("loop: %+v", resp.Results[0])
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
