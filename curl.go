package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
)

func curlQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\t", `\t`, "\r", `\r`, "\n", `\n`).Replace(s) + `"`
}

func execute(ctx context.Context, req request, in io.Reader, out, errOut io.Writer) int {
	// Private config files keep mapped URLs and injected headers out of argv,
	// while leaving stdin available for curl's normal @- request bodies.
	args := append([]string(nil), req.Args...)
	for index, content := range req.Private {
		f, err := os.CreateTemp("", "ocurla-curl-*")
		if err != nil {
			fmt.Fprintln(errOut, "ocurla: cannot create private curl config")
			return 2
		}
		defer os.Remove(f.Name())
		_, writeErr := io.WriteString(f, content)
		closeErr := f.Close()
		if writeErr != nil || closeErr != nil {
			fmt.Fprintln(errOut, "ocurla: cannot write private curl config")
			return 2
		}
		args[index] = f.Name()
	}
	cmd := exec.CommandContext(ctx, "curl", args...)
	cmd.Stdin = in
	stdout := newRedactor(out, req.Redactions)
	stderr := newRedactor(errOut, req.Redactions)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if os.Getenv("OCURLA_REDACT") == "0" {
		cmd.Stdout, cmd.Stderr = out, errOut
	}
	err := cmd.Run()
	outErr, errErr := stdout.flush(), stderr.flush()
	if ctx.Err() != nil {
		return 130
	}
	if err == nil && outErr == nil && errErr == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() >= 0 {
		return exit.ExitCode()
	}
	fmt.Fprintln(errOut, "ocurla: could not run curl or write its output; ensure curl is installed")
	return 2
}

type replacement struct{ from, to string }
type redactor struct {
	out     io.Writer
	rules   []replacement
	pending []byte
}

func newRedactor(out io.Writer, rules []replacement) *redactor {
	r := &redactor{out: out}
	for _, rule := range rules {
		if rule.from != "" && rule.from != rule.to {
			r.rules = append(r.rules, rule)
		}
	}
	sort.SliceStable(r.rules, func(i, j int) bool { return len(r.rules[i].from) > len(r.rules[j].from) })
	return r
}

func (r *redactor) Write(p []byte) (int, error) {
	r.pending = append(r.pending, p...)
	if err := r.drain(false); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (r *redactor) flush() error { return r.drain(true) }

func (r *redactor) drain(final bool) error {
	s := string(r.pending)
	var b strings.Builder
	i := 0
scan:
	for i < len(s) {
		for _, rule := range r.rules {
			if !final && len(s[i:]) < len(rule.from) && strings.HasPrefix(rule.from, s[i:]) {
				break scan
			}
			if strings.HasPrefix(s[i:], rule.from) {
				b.WriteString(rule.to)
				i += len(rule.from)
				continue scan
			}
		}
		b.WriteByte(s[i])
		i++
	}
	r.pending = append(r.pending[:0], s[i:]...)
	_, err := io.WriteString(r.out, b.String())
	return err
}
