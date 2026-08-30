package server

// Wire types for POST /execute. Field names are the contract with the app.

const (
	StatusOK           = "ok"
	StatusCompileError = "compile_error"
	StatusRuntimeError = "runtime_error"
	StatusTimeout      = "timeout"
	StatusError        = "error"

	TestPass    = "pass"
	TestFail    = "fail"
	TestError   = "error"
	TestTimeout = "timeout"
)

var Languages = map[string]bool{"python": true, "node": true, "go": true, "java": true, "sql": true}

type Request struct {
	ID        string `json:"id"`
	Language  string `json:"language"`
	Source    string `json:"source"`
	SQLSchema string `json:"sql_schema,omitempty"`
	Tests     []Test `json:"tests"`
	Limits    Limits `json:"limits"`
}

type Test struct {
	ID       string `json:"id"`
	Input    string `json:"input"`
	Expected string `json:"expected"`
	Weight   int    `json:"weight"`
	// Unordered compares SQL result rows as a multiset; ignored for code.
	Unordered bool `json:"unordered,omitempty"`
}

type Limits struct {
	CPUMs    int `json:"cpu_ms"`
	WallMs   int `json:"wall_ms"`
	MemMB    int `json:"mem_mb"`
	OutputKB int `json:"output_kb"`
	PIDs     int `json:"pids"`
}

// DefaultLimits fills zero-valued limit fields.
var DefaultLimits = Limits{CPUMs: 2000, WallMs: 5000, MemMB: 256, OutputKB: 64, PIDs: 64}

// MaxLimits bound what a caller may request.
var MaxLimits = Limits{CPUMs: 60000, WallMs: 60000, MemMB: 2048, OutputKB: 4096, PIDs: 512}

type Response struct {
	ID            string       `json:"id"`
	Status        string       `json:"status"`
	CompileOutput string       `json:"compile_output"`
	Results       []TestResult `json:"results"`
}

type TestResult struct {
	TestID     string `json:"test_id"`
	Status     string `json:"status"`
	StdoutHash string `json:"stdout_hash"`
	StderrTail string `json:"stderr_tail"`
	TimeMs     int64  `json:"time_ms"`
	MemKB      int64  `json:"mem_kb"`
}

func (l Limits) normalized() Limits {
	pick := func(v, def, max int) int {
		if v <= 0 {
			return def
		}
		if v > max {
			return max
		}
		return v
	}
	return Limits{
		CPUMs:    pick(l.CPUMs, DefaultLimits.CPUMs, MaxLimits.CPUMs),
		WallMs:   pick(l.WallMs, DefaultLimits.WallMs, MaxLimits.WallMs),
		MemMB:    pick(l.MemMB, DefaultLimits.MemMB, MaxLimits.MemMB),
		OutputKB: pick(l.OutputKB, DefaultLimits.OutputKB, MaxLimits.OutputKB),
		PIDs:     pick(l.PIDs, DefaultLimits.PIDs, MaxLimits.PIDs),
	}
}
