package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
)

const usage = `ocurla — curl with private endpoint mappings

Usage:
  ocurla config add [flags]       Guided setup; flags also work in scripts
  ocurla config list              List aliases without exposing targets or tokens
  ocurla config set NAME [flags]  Update mapping rules
  ocurla config remove NAME       Remove a mapping
  ocurla config path              Show the config file location
  ocurla [curl options] URL       Call a configured alias

Example:
  ocurla config add
  ocurla -sS http://localhost:3000/users
  ocurla --json '{"name":"Ada"}' http://localhost:3000/users

Configuration defaults to the OS user config directory, under ocurla/config.json.
Set OCURLA_CONFIG to override it. Run 'ocurla config add --help' for setup flags.
Curl options are passed to your installed curl, including redirects, tracing,
output files, proxies, and --next. Use --next between different mappings.
Rules apply to command-line URLs and headers. See README.md for details.
Set OCURLA_REDACT=0 for unchanged stdout/stderr (including binary data).
`

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) int {
	if len(args) == 0 || args[0] == "help" {
		fmt.Fprint(out, usage)
		return 0
	}
	path, err := configPath()
	if err == nil && args[0] == "config" {
		err = configure(path, args[1:], in, out)
	} else if err == nil {
		var cfg config
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			cfg, err = loadConfig(path)
		}
		if err == nil {
			var specs map[string]optionSpec
			specs, err = curlOptions(ctx)
			var req request
			if err == nil {
				req, err = prepare(cfg, args, os.LookupEnv, specs)
			}
			if err == nil {
				return execute(ctx, req, in, out, errOut)
			}
		}
	}
	if err != nil {
		if errors.Is(err, errHelp) {
			return 0
		}
		fmt.Fprintln(errOut, "ocurla:", err)
		return 2
	}
	return 0
}
