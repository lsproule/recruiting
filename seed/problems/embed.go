// Package problems embeds the platform's seed problem bank, which every org
// reads and none may edit. Each problem is one directory under _bank/ (the underscore keeps the go tool from reading the solution files as packages): a
// problem.json in the import format (README.md) minus its reference
// solutions, which sit next to it as ordinary source files, one per language,
// so they can be read, run, and edited as code rather than as JSON strings.
package problems

import "embed"

// FS holds the bank: _bank/<problem>/problem.json plus its solution files.
//
//go:embed all:_bank
var FS embed.FS

// SolutionLanguages maps a solution file's name to the language it proves.
// A file with any other name in a problem directory is an error, so a typo
// cannot quietly drop a language from the seed.
var SolutionLanguages = map[string]string{
	"solution.py":   "python",
	"solution.js":   "javascript",
	"solution.rb":   "ruby",
	"solution.php":  "php",
	"solution.go":   "go",
	"Solution.java": "java",
	"Solution.cs":   "csharp",
	"solution.cpp":  "cpp",
	"solution.c":    "c",
	"solution.rs":   "rust",
	"solution.sql":  "sql",
}
