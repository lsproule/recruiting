// Command recruiting is the single binary for every process mode of the
// recruiting platform.
package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"

	"recruiting/internal/config"
)

// mode is one runnable process mode of the binary.
type mode struct {
	summary string
	run     func(*slog.Logger, *config.Config, []string) error
}

func modes() map[string]mode {
	return map[string]mode{
		"serve":   {"run the HTTP server", runServe},
		"worker":  {"run the queue consumer", runWorker},
		"runner":  {"run the sandboxed execution service", runRunner},
		"migrate": {"apply database migrations", runMigrate},
		"admin":   {"administrative commands (create org / user)", runAdmin},
	}
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "recruiting: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("no mode given\n" + usage())
	}
	name := args[0]
	if name == "-h" || name == "--help" || name == "help" {
		fmt.Fprint(os.Stdout, usage())
		return nil
	}

	m, ok := modes()[name]
	if !ok {
		return fmt.Errorf("unknown mode %q\n%s", name, usage())
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("mode", name)

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	return m.run(logger, cfg, args[1:])
}

func usage() string {
	all := modes()
	names := make([]string, 0, len(all))
	for name := range all {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString("\nusage: recruiting <mode> [flags]\n\nmodes:\n")
	for _, name := range names {
		fmt.Fprintf(&b, "  %-8s %s\n", name, all[name].summary)
	}
	b.WriteString("\nconfiguration is read from the environment; see .env.example\n")
	return b.String()
}
