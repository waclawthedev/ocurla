package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

type config struct {
	Mappings []mapping `json:"mappings"`
}

type mapping struct {
	Name          string              `json:"name"`
	Alias         string              `json:"alias"`
	Target        string              `json:"target"`
	Token         string              `json:"token,omitempty"`
	TokenEnv      string              `json:"token_env,omitempty"`
	Headers       map[string]string   `json:"headers,omitempty"`
	HeaderEnv     map[string]string   `json:"header_env,omitempty"`
	RemoveHeaders []string            `json:"remove_headers,omitempty"`
	Query         map[string]string   `json:"query,omitempty"`
	AddQuery      map[string][]string `json:"add_query,omitempty"`
	RemoveQuery   []string            `json:"remove_query,omitempty"`
	Paths         []pathRule          `json:"paths,omitempty"`
}

type pathRule struct {
	From string `json:"from"`
	To   string `json:"to"`
}

var errHelp = flag.ErrHelp

func configPath() (string, error) {
	if path := os.Getenv("OCURLA_CONFIG"); path != "" {
		return path, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", errors.New("cannot find user config directory; set OCURLA_CONFIG")
	}
	return filepath.Join(dir, "ocurla", "config.json"), nil
}

func loadConfig(path string) (config, error) {
	var cfg config
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, errors.New("no configuration; run 'ocurla config add'")
		}
		return cfg, errors.New("cannot read configuration")
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 1024*1024))
	d.DisallowUnknownFields()
	if err := d.Decode(&cfg); err != nil {
		return cfg, errors.New("invalid config JSON")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return cfg, errors.New("unexpected data after config JSON")
	}
	return cfg, cfg.validate()
}

func (c config) validate() error {
	names, aliases := map[string]bool{}, map[string]bool{}
	for _, m := range c.Mappings {
		if m.Name == "" || strings.IndexFunc(m.Name, unicode.IsControl) >= 0 || names[m.Name] {
			return errors.New("mapping names must be nonempty and unique, without control characters")
		}
		names[m.Name] = true
		a, err := baseURL(m.Alias)
		if err != nil {
			return errors.New("mapping alias must be an HTTP(S) base URL without credentials, query, fragment, or dot segments")
		}
		key := strings.ToLower(a.Scheme+"://"+a.Host) + strings.TrimRight(a.EscapedPath(), "/")
		if aliases[key] {
			return errors.New("mapping aliases must be unique")
		}
		aliases[key] = true
		if _, err := baseURL(m.Target); err != nil {
			return errors.New("mapping target must be an HTTP(S) base URL without credentials, query, fragment, or dot segments")
		}
		if m.Token != "" && m.TokenEnv != "" {
			return errors.New("choose either token or token_env for a mapping")
		}
		if strings.ContainsAny(m.Token, "\r\n\x00") || strings.ContainsAny(m.TokenEnv, "=\r\n\x00") {
			return errors.New("invalid token or environment variable name")
		}
		seenHeaders := map[string]bool{}
		for _, fields := range []map[string]string{m.Headers, m.HeaderEnv} {
			for key, value := range fields {
				lower := strings.ToLower(key)
				if !validHeaderName(key) || strings.ContainsAny(value, "\r\n\x00") || seenHeaders[lower] {
					return errors.New("invalid or duplicate header rule")
				}
				seenHeaders[lower] = true
			}
		}
		for _, key := range m.RemoveHeaders {
			if !validHeaderName(key) {
				return errors.New("invalid header name to remove")
			}
		}
		for _, env := range m.HeaderEnv {
			if env == "" || strings.ContainsAny(env, "=\r\n\x00") {
				return errors.New("invalid header environment variable name")
			}
		}
		for _, rule := range m.Paths {
			for _, path := range []string{rule.From, rule.To} {
				if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "?#\r\n\x00") {
					return errors.New("path rules must start with / and contain no query or fragment")
				}
				decoded, err := url.PathUnescape(path)
				if err != nil || unsafePath(decoded) {
					return errors.New("invalid escaped path rule")
				}
			}
		}
	}
	return nil
}

func validHeaderName(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c)) {
			return false
		}
	}
	return true
}

func baseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(raw, "\r\n\x00?#") || unsafePath(u.Path) {
		return nil, errors.New("invalid base URL")
	}
	return u, nil
}

func unsafePath(path string) bool {
	if strings.Contains(path, "\\") {
		return true
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "." || segment == ".." {
			return true
		}
	}
	return false
}

func saveConfig(path string, cfg config) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return errors.New("cannot create config directory")
	}
	f, err := os.CreateTemp(dir, ".ocurla-*")
	if err != nil {
		return errors.New("cannot create config file")
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(data, '\n')); err != nil {
		f.Close()
		return errors.New("cannot write config file")
	}
	if err = f.Close(); err != nil {
		return errors.New("cannot close config file")
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return errors.New("cannot replace config file")
	}
	return nil
}

func configure(path string, args []string, in io.Reader, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("expected config add, list, remove, or path")
	}
	if args[0] == "path" && len(args) == 1 {
		fmt.Fprintln(out, path)
		return nil
	}
	if args[0] == "add" {
		return editMapping(path, "", args[1:], in, out)
	}
	if args[0] == "set" {
		if len(args) < 3 {
			return errors.New("usage: ocurla config set NAME [flags]")
		}
		return editMapping(path, args[1], args[2:], in, out)
	}
	if args[0] != "list" && args[0] != "remove" {
		return errors.New("unknown config command")
	}
	cfg, err := loadConfig(path)
	if err != nil {
		return err
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return errors.New("usage: ocurla config list")
		}
		for _, m := range cfg.Mappings {
			auth := "no auth"
			if m.Token != "" || m.TokenEnv != "" {
				auth = "bearer auth"
			}
			fmt.Fprintf(out, "%s\t%s\t%s\n", m.Name, m.Alias, auth)
		}
	case "remove":
		if len(args) != 2 {
			return errors.New("usage: ocurla config remove NAME")
		}
		for i, m := range cfg.Mappings {
			if m.Name == args[1] {
				cfg.Mappings = append(cfg.Mappings[:i], cfg.Mappings[i+1:]...)
				return saveConfig(path, cfg)
			}
		}
		return errors.New("mapping not found")
	}
	return nil
}

func editMapping(path, existingName string, args []string, in io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("ocurla config add", flag.ContinueOnError)
	fs.SetOutput(out)
	var m mapping
	var cfg config
	if _, err := os.Stat(path); err == nil {
		cfg, err = loadConfig(path)
		if err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot access configuration")
	}
	index := -1
	if existingName != "" {
		for i, entry := range cfg.Mappings {
			if entry.Name == existingName {
				m, index = entry, i
				break
			}
		}
		if index < 0 {
			return errors.New("mapping not found")
		}
	}
	fs.StringVar(&m.Name, "name", m.Name, "mapping name")
	fs.StringVar(&m.Alias, "alias", m.Alias, "public base URL, e.g. http://localhost:3000")
	fs.StringVar(&m.Target, "target", m.Target, "real API base URL")
	fs.StringVar(&m.TokenEnv, "token-env", m.TokenEnv, "environment variable containing the bearer token")
	tokenStdin := fs.Bool("token-stdin", false, "read bearer token from stdin and store in config")
	noAuth := fs.Bool("no-auth", false, "clear the bearer token and token environment variable")
	clearRules := fs.Bool("clear-rules", false, "clear all header, query, and path rules before applying flags")
	var headers, headerEnvs, removeHeaders, queries, addQueries, removeQueries, paths stringFlags
	fs.Var(&headers, "header", "set/replace header NAME=VALUE (repeatable; empty value sends an empty header)")
	fs.Var(&headerEnvs, "header-env", "set/replace header NAME=ENV_VAR from environment (repeatable)")
	fs.Var(&removeHeaders, "remove-header", "remove header NAME (repeatable)")
	fs.Var(&queries, "query", "set/replace URL query NAME=VALUE (repeatable)")
	fs.Var(&addQueries, "add-query", "append URL query NAME=VALUE (repeatable)")
	fs.Var(&removeQueries, "remove-query", "remove URL query NAME (repeatable)")
	fs.Var(&paths, "path-rewrite", "rewrite alias-relative path prefix FROM=TO (repeatable; first match wins)")
	fs.VisitAll(func(f *flag.Flag) {
		if f.Name == "target" || f.Name == "token-env" {
			f.DefValue = ""
		}
	})
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected config add arguments")
	}
	tokenEnvProvided := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "token-env" {
			tokenEnvProvided = true
		}
	})
	if *tokenStdin && tokenEnvProvided && m.TokenEnv != "" {
		return errors.New("choose either --token-stdin or --token-env")
	}
	if *noAuth && (*tokenStdin || tokenEnvProvided) {
		return errors.New("--no-auth cannot be combined with token flags")
	}
	if *noAuth {
		m.Token, m.TokenEnv = "", ""
	}
	if tokenEnvProvided {
		m.Token = ""
	}
	if *tokenStdin {
		m.TokenEnv = ""
	}
	if *clearRules {
		m.Headers = nil
		m.HeaderEnv = nil
		m.RemoveHeaders = nil
		m.Query = nil
		m.AddQuery = nil
		m.RemoveQuery = nil
		m.Paths = nil
	}
	if err := applyRuleFlags(&m, headers, headerEnvs, removeHeaders, queries, addQueries, removeQueries, paths); err != nil {
		return err
	}
	if len(args) == 0 {
		reader := bufio.NewReader(in)
		ask := func(label, fallback string) (string, error) {
			fmt.Fprint(out, label)
			if fallback != "" {
				fmt.Fprintf(out, " [%s]", fallback)
			}
			fmt.Fprint(out, ": ")
			line, err := reader.ReadString('\n')
			if err != nil && (err != io.EOF || len(line) == 0) {
				return "", errors.New("setup input ended; configuration was not saved")
			}
			line = strings.TrimSpace(line)
			if line == "" {
				line = fallback
			}
			return line, nil
		}
		fields := []struct {
			label, fallback string
			dest            *string
		}{
			{"Mapping name", "default", &m.Name},
			{"Alias base URL", "http://localhost:3000", &m.Alias},
			{"Real API base URL", "", &m.Target},
			{"Token environment variable (blank for no auth)", "", &m.TokenEnv},
		}
		for _, field := range fields {
			value, err := ask(field.label, field.fallback)
			if err != nil {
				return err
			}
			*field.dest = value
		}
	}
	if m.Name == "" || m.Alias == "" || m.Target == "" {
		return errors.New("--name, --alias, and --target are required; omit all flags for guided setup")
	}
	if *tokenStdin {
		data, err := io.ReadAll(io.LimitReader(in, 65537))
		if err != nil || len(data) > 65536 {
			return errors.New("cannot read token (maximum 64 KiB)")
		}
		m.Token = strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
		if m.Token == "" {
			return errors.New("token must not be empty")
		}
	}
	for i, existing := range cfg.Mappings {
		if existing.Name == m.Name && i != index {
			return errors.New("mapping already exists; remove it before replacing it")
		}
	}
	if index < 0 {
		cfg.Mappings = append(cfg.Mappings, m)
	} else {
		cfg.Mappings[index] = m
	}
	if err := saveConfig(path, cfg); err != nil {
		return err
	}
	fmt.Fprintln(out, "Mapping saved. Run 'ocurla config list' to see aliases.")
	return nil
}

type stringFlags []string

func (s *stringFlags) String() string         { return "" }
func (s *stringFlags) Set(value string) error { *s = append(*s, value); return nil }

func applyRuleFlags(m *mapping, headers, envs, removes, queries, adds, removeQueries, paths []string) error {
	if m.Headers == nil {
		m.Headers = map[string]string{}
	}
	if m.HeaderEnv == nil {
		m.HeaderEnv = map[string]string{}
	}
	if m.Query == nil {
		m.Query = map[string]string{}
	}
	if m.AddQuery == nil {
		m.AddQuery = map[string][]string{}
	}
	removeHeader := func(name string) {
		for key := range m.Headers {
			if strings.EqualFold(key, name) {
				delete(m.Headers, key)
			}
		}
		for key := range m.HeaderEnv {
			if strings.EqualFold(key, name) {
				delete(m.HeaderEnv, key)
			}
		}
	}
	for _, name := range removes {
		removeHeader(name)
		m.RemoveHeaders = append(m.RemoveHeaders, name)
	}
	for _, name := range removeQueries {
		delete(m.Query, name)
		delete(m.AddQuery, name)
		m.RemoveQuery = append(m.RemoveQuery, name)
	}
	for i, values := range [][]string{headers, envs, queries, adds, paths} {
		for _, value := range values {
			key, val, ok := strings.Cut(value, "=")
			if !ok || key == "" {
				return errors.New("rule must use NAME=VALUE syntax")
			}
			switch i {
			case 0:
				removeHeader(key)
				m.Headers[key] = val
			case 1:
				removeHeader(key)
				m.HeaderEnv[key] = val
			case 2:
				m.Query[key] = val
			case 3:
				m.AddQuery[key] = append(m.AddQuery[key], val)
			case 4:
				m.Paths = append(m.Paths, pathRule{key, val})
			}
		}
	}
	return nil
}
