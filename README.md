# ocurla

Use curl with an alias URL. Ocurla replaces it with your real API URL, adds
credentials, and runs curl. No local server is needed.

**Experimental:** bugs or incorrect configuration can expose credentials or
modify/delete remote data. Test with disposable data and limited credentials.

Written in Go with no third-party Go dependencies. Requires Go 1.22+ to build
and curl installed.

## Quick start

```sh
go build -o ocurla .

export WORK_API_TOKEN='your-token'
./ocurla config add \
  --name work \
  --alias http://localhost:3000 \
  --target https://api.example.com/v1 \
  --token-env WORK_API_TOKEN

./ocurla http://localhost:3000/users
```

This calls `https://api.example.com/v1/users` with your bearer token.
`work` is the name of this saved mapping.

Prefer guided setup? Run `./ocurla config add` without flags.

## Customize requests

Set headers, rewrite paths, and change URL query parameters:

```sh
./ocurla config set work \
  --header 'X-Tenant=team-a' \
  --remove-header X-Debug \
  --path-rewrite '/people=/users' \
  --query 'region=eu' \
  --remove-query debug
```

Use your usual curl options:

```sh
./ocurla -sS http://localhost:3000/users
./ocurla --json '{"name":"Ada"}' http://localhost:3000/users
```

## Configuration

```sh
./ocurla config list        # Show saved mapping names and aliases
./ocurla config path        # Show where the JSON config is stored
./ocurla config add --help  # See all configuration options
```

Config lives in your OS user config directory. Set `OCURLA_CONFIG` to use another
file. Tokens can come from environment variables or be saved with `--token-stdin`.

## Things to know

- Rules apply to command-line URLs and headers, not entries inside curl config files.
- Use `--next` between URLs belonging to different mappings. Unmatched URLs pass through.
- Terminal output is redacted by default. Set `OCURLA_REDACT=0` for exact output,
  including binary data. Files written directly by curl aren't redacted.
- This keeps credentials out of normal commands, but doesn't hide them from an AI
  that can read your config or environment.

## License and risk

Licensed under [MIT](LICENSE), provided "as is", without warranty, with the
liability limitations stated in that license, subject to applicable law.
Security, correctness, and prevention of data loss or disclosure are not
guaranteed. Read [security limitations and reporting guidance](SECURITY.md).
