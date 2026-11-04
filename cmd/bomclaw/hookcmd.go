package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/ngocp/goterm-control/internal/plugins"
)

// `bomclaw hook <pre_tool|post_tool> --backend <claude|codex|opencode>` is not
// for people. It is what each CLI runs around a tool call when a plugin has
// hooks: the gateway registers it per turn, and it dispatches to the plugins'
// hook commands. See internal/plugins/hooks.go.
func runHook(args []string) {
	if len(args) == 0 || (args[0] != plugins.PreTool && args[0] != plugins.PostTool) {
		fmt.Fprintln(os.Stderr, "Usage: bomclaw hook <pre_tool|post_tool> --backend <claude|codex|opencode>  (payload on stdin)")
		os.Exit(1)
	}
	fs := flag.NewFlagSet("hook", flag.ExitOnError)
	backend := fs.String("backend", "claude", "Which CLI is calling")
	fs.Parse(args[1:])
	os.Exit(plugins.RunHooks(args[0], *backend, os.Stdin, os.Stdout, os.Stderr))
}
