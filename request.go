package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

type request struct {
	Args       []string
	Private    map[int]string // argv index -> contents of a private curl config
	Redactions []replacement
}

type optionSpec struct {
	name  string
	value bool
}
type curlOption struct {
	name, value string
	args        []string
}

// Ask the installed curl about its option syntax, so new curl flags don't need
// an ocurla release. We only interpret URL operands, headers, and --next.
func curlOptions(ctx context.Context) (map[string]optionSpec, error) {
	data, err := exec.CommandContext(ctx, "curl", "-q", "--help", "all").Output()
	if err != nil {
		return nil, errors.New("cannot inspect curl options; ensure curl is installed")
	}
	return parseOptionHelp(string(data))
}

func parseOptionHelp(help string) (map[string]optionSpec, error) {
	specs := map[string]optionSpec{}
	lineRE := regexp.MustCompile(`^\s*(?:(-[^\s,]),\s+)?(--[a-zA-Z0-9-]+)(.*)$`)
	for _, line := range strings.Split(help, "\n") {
		m := lineRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		// A placeholder follows one space; the description is separated by 2+.
		tail := m[3]
		value := strings.HasPrefix(tail, " ") && len(tail) > 1 && tail[1] != ' '
		spec := optionSpec{m[2], value}
		specs[m[2]] = spec
		if m[1] != "" {
			specs[m[1]] = spec
		}
	}
	if _, ok := specs["--url"]; !ok {
		return nil, errors.New("cannot understand installed curl's option help")
	}
	return specs, nil
}

func parseArgs(args []string, specs map[string]optionSpec) ([]curlOption, error) {
	var result []curlOption
	positional := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if positional || !strings.HasPrefix(arg, "-") || arg == "-" {
			raw := []string{arg}
			if positional {
				raw = []string{"--url", arg}
			}
			result = append(result, curlOption{"--url", arg, raw})
			continue
		}
		if arg == "--" {
			positional = true
			continue
		}
		if strings.HasPrefix(arg, "--") {
			name, value, attached := strings.Cut(arg, "=")
			spec, ok := specs[name]
			if !ok {
				var matches []optionSpec
				for key, candidate := range specs {
					if strings.HasPrefix(key, name) {
						matches = append(matches, candidate)
					}
				}
				if len(matches) == 1 {
					spec, ok = matches[0], true
				}
			}
			if !ok && strings.HasPrefix(name, "--no-") {
				spec, ok = specs["--"+strings.TrimPrefix(name, "--no-")]
				ok = ok && !spec.value
				spec.name = name
			}
			if !ok {
				candidate, exists := specs["--no-"+strings.TrimPrefix(name, "--")]
				if exists && !candidate.value {
					spec, ok = optionSpec{name, false}, true
				}
			}
			if !ok {
				result = append(result, curlOption{name, "", []string{arg}})
				continue
			}
			raw := []string{arg}
			if spec.value && !attached {
				if spec.name == "--help" && i+1 == len(args) {
					result = append(result, curlOption{spec.name, "", raw})
					continue
				}
				i++
				if i >= len(args) {
					return nil, fmt.Errorf("missing value for %s", name)
				}
				value = args[i]
				raw = append(raw, value)
			}
			result = append(result, curlOption{spec.name, value, raw})
			continue
		}
		for j := 1; j < len(arg); j++ {
			name := "-" + string(arg[j])
			spec, ok := specs[name]
			if !ok {
				result = append(result, curlOption{name, "", []string{name}})
				continue
			}
			value := ""
			raw := []string{name}
			if spec.value {
				value = arg[j+1:]
				if value == "" {
					if spec.name == "--help" && i+1 == len(args) {
						result = append(result, curlOption{spec.name, "", raw})
						break
					}
					i++
					if i >= len(args) {
						return nil, fmt.Errorf("missing value for %s", name)
					}
					value = args[i]
				}
				raw = append(raw, value)
			}
			result = append(result, curlOption{spec.name, value, raw})
			if spec.value {
				break
			}
		}
	}
	return result, nil
}

func prepare(cfg config, args []string, lookup func(string) (string, bool), specs map[string]optionSpec) (request, error) {
	req := request{Private: map[int]string{}}
	if err := cfg.validate(); err != nil {
		return req, err
	}
	options, err := parseArgs(args, specs)
	if err != nil {
		return req, err
	}
	for start := 0; start <= len(options); {
		end := start
		for end < len(options) && options[end].name != "--next" {
			end++
		}
		if err := prepareGroup(&req, cfg, options[start:end], lookup); err != nil {
			return request{}, err
		}
		if end == len(options) {
			break
		}
		req.Args = append(req.Args, options[end].args...)
		start = end + 1
	}
	return req, nil
}

func (r *request) addPrivate(content string) {
	r.Args = append(r.Args, "--config", "")
	r.Private[len(r.Args)-1] = content
}

func matchMapping(cfg config, raw string) (*mapping, *url.URL, *url.URL) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, nil, nil
	}
	var selected *mapping
	var alias *url.URL
	best := -1
	for i := range cfg.Mappings {
		m := &cfg.Mappings[i]
		a, _ := baseURL(m.Alias)
		prefix := strings.TrimRight(a.EscapedPath(), "/")
		if !strings.EqualFold(a.Scheme, u.Scheme) || !strings.EqualFold(a.Host, u.Host) {
			continue
		}
		if u.EscapedPath() != prefix && !strings.HasPrefix(u.EscapedPath(), prefix+"/") {
			continue
		}
		if len(prefix) > best {
			selected, alias, best = m, a, len(prefix)
		}
	}
	return selected, alias, u
}

func prepareGroup(req *request, cfg config, options []curlOption, lookup func(string) (string, bool)) error {
	var selected *mapping
	unmatched := false
	for _, option := range options {
		if option.name != "--url" {
			continue
		}
		m, _, _ := matchMapping(cfg, option.value)
		if m == nil {
			unmatched = true
			continue
		}
		if selected != nil && selected.Name != m.Name {
			return errors.New("different mappings need separate curl transfers; put --next between them")
		}
		selected = m
	}
	if selected == nil {
		for _, option := range options {
			req.Args = append(req.Args, option.args...)
		}
		return nil
	}
	if unmatched {
		return errors.New("mapped and unmapped URLs need separate curl transfers; put --next between them")
	}
	headers, redactions, err := mappingHeaders(*selected, lookup)
	if err != nil {
		return err
	}
	req.Redactions = append(req.Redactions, redactions...)
	for _, option := range options {
		switch option.name {
		case "--url":
			_, alias, u := matchMapping(cfg, option.value)
			target, _ := baseURL(selected.Target)
			tail := strings.TrimPrefix(u.EscapedPath(), strings.TrimRight(alias.EscapedPath(), "/"))
			// Path rules apply to the escaped path remaining after the alias prefix.
			for _, rule := range selected.Paths {
				if tail == rule.From || strings.HasPrefix(tail, strings.TrimRight(rule.From, "/")+"/") {
					tail = strings.TrimRight(rule.To, "/") + strings.TrimPrefix(tail, strings.TrimRight(rule.From, "/"))
					break
				}
			}
			path := strings.TrimRight(target.EscapedPath(), "/") + tail
			target.Path, err = url.PathUnescape(path)
			if err != nil {
				return errors.New("invalid escaped path in mapping")
			}
			target.RawPath = path
			target.RawQuery, target.ForceQuery, target.Fragment, target.RawFragment = u.RawQuery, u.ForceQuery, u.Fragment, u.RawFragment
			if u.User != nil {
				target.User = u.User
			}
			if len(selected.Query) != 0 || len(selected.AddQuery) != 0 || len(selected.RemoveQuery) != 0 {
				q, err := url.ParseQuery(u.RawQuery)
				if err != nil {
					return errors.New("cannot apply query rules to malformed URL query")
				}
				for _, key := range selected.RemoveQuery {
					q.Del(key)
				}
				for key, value := range selected.Query {
					q.Set(key, value)
				}
				for key, values := range selected.AddQuery {
					for _, value := range values {
						q.Add(key, value)
					}
				}
				target.RawQuery = q.Encode()
			}
			req.addPrivate("url = " + curlQuote(target.String()) + "\n")
			req.Redactions = append(req.Redactions,
				replacement{strings.TrimRight(selected.Target, "/"), strings.TrimRight(selected.Alias, "/")},
				replacement{target.Host, alias.Host}, replacement{target.Hostname(), alias.Hostname()})
		case "--header":
			name := headerName(option.value)
			if _, overridden := headers[strings.ToLower(name)]; !overridden {
				req.Args = append(req.Args, option.args...)
			}
		default:
			req.Args = append(req.Args, option.args...)
		}
	}
	if len(headers) > 0 {
		keys := make([]string, 0, len(headers))
		for key := range headers {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		var b strings.Builder
		for _, key := range keys {
			fmt.Fprintf(&b, "header = %s\n", curlQuote(headers[key]))
		}
		req.addPrivate(b.String())
	}
	return nil
}

func headerName(header string) string {
	if index := strings.IndexAny(header, ":;"); index >= 0 {
		header = header[:index]
	}
	return strings.TrimSpace(header)
}

func mappingHeaders(m mapping, lookup func(string) (string, bool)) (map[string]string, []replacement, error) {
	headers := map[string]string{}
	var redactions []replacement
	for _, key := range m.RemoveHeaders {
		headers[strings.ToLower(key)] = key + ":"
	}
	for key, value := range m.Headers {
		delimiter := ": "
		if value == "" {
			delimiter = ";"
		}
		headers[strings.ToLower(key)] = key + delimiter + value
		redactions = append(redactions, replacement{value, "[REDACTED]"})
	}
	for key, env := range m.HeaderEnv {
		value, ok := lookup(env)
		if !ok || value == "" {
			return nil, nil, errors.New("configured header environment variable is unset or empty")
		}
		if strings.ContainsAny(value, "\r\n\x00") {
			return nil, nil, errors.New("configured header contains invalid characters")
		}
		headers[strings.ToLower(key)] = key + ": " + value
		redactions = append(redactions, replacement{value, "[REDACTED]"})
	}
	token := m.Token
	if m.TokenEnv != "" {
		var ok bool
		token, ok = lookup(m.TokenEnv)
		if !ok || token == "" {
			return nil, nil, errors.New("configured token environment variable is unset or empty")
		}
	}
	if strings.ContainsAny(token, "\r\n\x00") {
		return nil, nil, errors.New("configured bearer token contains invalid characters")
	}
	if token != "" {
		headers["authorization"] = "Authorization: Bearer " + token
		redactions = append(redactions, replacement{token, "[REDACTED]"})
	}
	return headers, redactions, nil
}
