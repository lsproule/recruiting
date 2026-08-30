package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"

	"recruiting/internal/config"
	"recruiting/internal/service"
)

// adminCommand is one subcommand of the admin mode.
type adminCommand struct {
	summary string
	run     func(io.Writer, *slog.Logger, *config.Config, []string) error
}

func adminCommands() map[string]adminCommand {
	return map[string]adminCommand{
		"create-org": {"create an org and its first admin", createOrg},
	}
}

// RunAdmin dispatches an admin subcommand.
func RunAdmin(logger *slog.Logger, cfg *config.Config, args []string) error {
	return runAdminCmd(os.Stdout, logger, cfg, args)
}

func runAdminCmd(out io.Writer, logger *slog.Logger, cfg *config.Config, args []string) error {
	cmds := adminCommands()
	if len(args) == 0 {
		return errors.New("no admin command given\n" + adminUsage(cmds))
	}
	name := args[0]
	if name == "-h" || name == "--help" || name == "help" {
		fmt.Fprint(out, adminUsage(cmds))
		return nil
	}
	c, ok := cmds[name]
	if !ok {
		return fmt.Errorf("unknown admin command %q\n%s", name, adminUsage(cmds))
	}
	return c.run(out, logger, cfg, args[1:])
}

func adminUsage(cmds map[string]adminCommand) string {
	names := make([]string, 0, len(cmds))
	for name := range cmds {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("\nusage: recruiting admin <command> [flags]\n\ncommands:\n")
	for _, name := range names {
		fmt.Fprintf(&b, "  %-12s %s\n", name, cmds[name].summary)
	}
	return b.String()
}

// passwordSetLink is where a new account chooses its first password; the app
// surface's reset page consumes the same one-time tokens.
func passwordSetLink(baseURL, token string) string {
	return strings.TrimRight(baseURL, "/") + "/app/reset/" + token
}

// orgSlug derives an org's URL slug from its display name.
func orgSlug(name string) string { return service.Slugify(name) }
