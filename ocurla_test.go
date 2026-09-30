package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func testConfig() config {
	return config{Mappings: []mapping{{Name: "api", Alias: "http://localhost:3000", Target: "https://private.example/v1", Token: "secret-token"}}}
}
func noEnv(string) (string, bool) { return "", false }
func testSpecs(t *testing.T) map[string]optionSpec {
	t.Helper()
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not installed")
	}
	specs, err := curlOptions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return specs
}
func privateContent(req request) string {
	var b strings.Builder
	for i := range req.Args {
		b.WriteString(req.Private[i])
	}
	return b.String()
}

func TestCurlSyntax(t *testing.T) {
	specs := testSpecs(t)
	for _, name := range []string{"--url", "--header", "--proxy", "--request", "--output", "--write-out", "--config"} {
		if !specs[name].value {
			t.Errorf("%s should take a value", name)
		}
	}
	for _, name := range []string{"--verbose", "--location", "--next", "--compressed"} {
		if specs[name].value || specs[name].name == "" {
			t.Errorf("incorrect boolean: %s", name)
		}
	}
	args := []string{"-qsvL", "-XPOST", "--proxy", "http://proxy:80", "-o", "result.bin", "-w", "%{http_code}", "--url", "http://localhost:3000/users"}
	req, err := prepare(testConfig(), args, noEnv, specs)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-q", "-s", "-v", "-L", "-X", "POST", "--proxy", "http://proxy:80", "-o", "result.bin", "-w", "%{http_code}", "--config", "", "--config", ""}
	if !reflect.DeepEqual(req.Args, want) {
		t.Fatalf("args: %#v", req.Args)
	}
	for _, arg := range req.Args {
		if strings.Contains(arg, "private") || strings.Contains(arg, "secret-token") {
			t.Fatal("private value in argv")
		}
	}
	for _, args := range [][]string{{"--help"}, {"-h"}, {"--version"}, {"--made-up-option"}} {
		if _, err := prepare(config{}, args, noEnv, specs); err != nil {
			t.Fatalf("must pass to curl: %v", err)
		}
	}
	req, err = prepare(config{}, []string{"--", "--not-an-option"}, noEnv, specs)
	if err != nil || !reflect.DeepEqual(req.Args, []string{"--url", "--not-an-option"}) {
		t.Fatal("lost end-of-options semantics")
	}
}

func TestURLAndHeaderRules(t *testing.T) {
	cfg := testConfig()
	m := &cfg.Mappings[0]
	m.Alias = "http://localhost:3000/public"
	m.Headers = map[string]string{"X-Tenant": "private-tenant", "X-Empty": ""}
	m.HeaderEnv = map[string]string{"X-Key": "API_KEY"}
	m.RemoveHeaders = []string{"X-Debug"}
	m.Paths = []pathRule{{"/old", "/new"}}
	m.Query = map[string]string{"tenant": "private-tenant"}
	m.AddQuery = map[string][]string{"tag": {"extra", "last"}}
	m.RemoveQuery = []string{"debug"}
	req, err := prepare(cfg, []string{"-H", "authorization: fake", "-H", "x-tenant: wrong", "-H", "X-Debug: true", "-H", "Accept: application/json", "http://localhost:3000/public/old/a%2Fb?tag=first&debug=1&tenant=wrong&x=a%20b"}, func(string) (string, bool) { return "key-secret", true }, testSpecs(t))
	if err != nil {
		t.Fatal(err)
	}
	content := privateContent(req)
	for _, want := range []string{"https://private.example/v1/new/a%2Fb?tag=first&tag=extra&tag=last&tenant=private-tenant&x=a+b", "Authorization: Bearer secret-token", "X-Tenant: private-tenant", "X-Key: key-secret", "X-Debug:", "X-Empty;"} {
		if !strings.Contains(content, want) {
			t.Errorf("missing %q in %s", want, content)
		}
	}
	for _, arg := range req.Args {
		if strings.Contains(arg, "wrong") || strings.Contains(arg, "fake") || strings.Contains(arg, "X-Debug") {
			t.Fatalf("overridden header remained: %s", arg)
		}
	}
	if !strings.Contains(strings.Join(req.Args, " "), "Accept: application/json") {
		t.Fatal("lost user header")
	}
}

func TestMultipleTransfers(t *testing.T) {
	cfg := testConfig()
	cfg.Mappings = append(cfg.Mappings, mapping{Name: "admin", Alias: "http://localhost:3000/admin", Target: "https://admin.example/v2", Token: "admin-token"})
	specs := testSpecs(t)
	req, err := prepare(cfg, []string{"http://localhost:3000/users", "--next", "http://localhost:3000/admin/users"}, noEnv, specs)
	if err != nil {
		t.Fatal(err)
	}
	content := privateContent(req)
	for _, value := range []string{"https://private.example/v1/users", "https://admin.example/v2/users", "secret-token", "admin-token"} {
		if !strings.Contains(content, value) {
			t.Fatalf("missing %s", value)
		}
	}
	for _, args := range [][]string{
		{"http://localhost:3000/users", "http://localhost:3000/admin/users"},
		{"http://localhost:3000/users", "http://unmapped.example"},
	} {
		if _, err := prepare(cfg, args, noEnv, specs); err == nil {
			t.Fatal("mixed credentials allowed without --next")
		}
	}
	req, err = prepare(cfg, []string{"http://localhost:3000/users", "http://localhost:3000/teams"}, noEnv, specs)
	if err != nil || len(req.Private) != 3 {
		t.Fatalf("same mapping multi URL: %v", err)
	}
	raw := []string{"--compressed", "file:///tmp/example"}
	req, err = prepare(cfg, raw, noEnv, specs)
	if err != nil || !reflect.DeepEqual(req.Args, raw) {
		t.Fatal("unmapped request changed")
	}
}

func TestMissingEnvironmentToken(t *testing.T) {
	cfg := testConfig()
	cfg.Mappings[0].Token = ""
	cfg.Mappings[0].TokenEnv = "MISSING"
	if _, err := prepare(cfg, []string{"http://localhost:3000"}, noEnv, testSpecs(t)); err == nil {
		t.Fatal("accepted missing token")
	}
}

func TestRedactionAcrossEveryBoundary(t *testing.T) {
	input := "https://private.example/v1/users secret-token private.example trailing sec"
	want := "http://localhost:3000/users [REDACTED] localhost:3000 trailing sec"
	req, err := prepare(testConfig(), []string{"http://localhost:3000"}, noEnv, testSpecs(t))
	if err != nil {
		t.Fatal(err)
	}
	for split := 0; split <= len(input); split++ {
		var out bytes.Buffer
		r := newRedactor(&out, req.Redactions)
		for _, part := range []string{input[:split], input[split:]} {
			if _, err := r.Write([]byte(part)); err != nil {
				t.Fatal(err)
			}
		}
		if err := r.flush(); err != nil {
			t.Fatal(err)
		}
		if out.String() != want {
			t.Fatalf("split %d: %q", split, out.String())
		}
	}
	var out bytes.Buffer
	r := newRedactor(&out, req.Redactions)
	for _, b := range []byte(input) {
		_, _ = r.Write([]byte{b})
	}
	_ = r.flush()
	if out.String() != want {
		t.Fatalf("byte stream: %q", out.String())
	}
}

func TestConfigure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ocurla", "config.json")
	var out bytes.Buffer
	err := configure(path, []string{"add"}, strings.NewReader("demo\n\nhttps://private.example/v1\nAPI_TOKEN\n"), &out)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mappings[0].TokenEnv != "API_TOKEN" {
		t.Fatal("wizard token source")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	out.Reset()
	if err := configure(path, []string{"list"}, nil, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "private") || strings.Contains(out.String(), "API_TOKEN") {
		t.Fatal("list exposed configuration")
	}
	err = configure(path, []string{"set", "demo", "--token-stdin", "--header", "X-Tenant=tenant", "--remove-header", "X-Debug", "--query", "region=eu", "--add-query", "tag=a", "--path-rewrite", "/old=/new"}, strings.NewReader("stored-secret\n"), &out)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err = loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	m := cfg.Mappings[0]
	if m.Token != "stored-secret" || m.TokenEnv != "" || m.Headers["X-Tenant"] != "tenant" || m.Query["region"] != "eu" || len(m.Paths) != 1 {
		t.Fatalf("incorrect set: %#v", m)
	}
	if err := configure(path, []string{"set", "demo", "--no-auth", "--clear-rules"}, nil, &out); err != nil {
		t.Fatal(err)
	}
	cfg, _ = loadConfig(path)
	if cfg.Mappings[0].Token != "" || len(cfg.Mappings[0].Headers) != 0 {
		t.Fatal("clear failed")
	}
	if err := configure(path, []string{"remove", "demo"}, nil, &out); err != nil {
		t.Fatal(err)
	}
	cfg, _ = loadConfig(path)
	if len(cfg.Mappings) != 0 {
		t.Fatal("remove failed")
	}
}

func TestInvalidConfigDoesNotExposeValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	for _, content := range []string{`{"secret-token":true}`, `{"mappings":[]} trailing-secret`, `{"mappings":[{"name":"x","alias":"http://localhost:3000","target":"https://%secret"}]}`} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := loadConfig(path)
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe error: %v", err)
		}
	}
}

func TestCurlIntegration(t *testing.T) {
	specs := testSpecs(t)
	type received struct{ path, query, method, auth, tenant, debug, body, userAgent string }
	requests := make(chan received, 20)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- received{r.URL.EscapedPath(), r.URL.RawQuery, r.Method, r.Header.Get("Authorization"), r.Header.Get("X-Tenant"), r.Header.Get("X-Debug"), string(body), r.UserAgent()}
		if r.URL.Path == "/api/fail" {
			w.WriteHeader(418)
			return
		}
		if r.URL.Path == "/api/redirect" {
			http.Redirect(w, r, server.URL+"/api/landing", 302)
			return
		}
		_, _ = io.WriteString(w, server.URL+"/api/users secret-token")
	}))
	defer server.Close()
	cfg := config{Mappings: []mapping{{Name: "test", Alias: "http://localhost:3000", Target: server.URL + "/api", Token: "secret-token", Headers: map[string]string{"X-Tenant": "tenant"}, RemoveHeaders: []string{"X-Debug"}, Query: map[string]string{"region": "eu"}}}}
	home := t.TempDir()
	// Preserve the user's curlrc. -q still disables it when the caller requests it.
	if err := os.WriteFile(filepath.Join(home, ".curlrc"), []byte("user-agent = \"curlrc-agent\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CURL_HOME", home)
	call := func(args []string, body string) (int, string, string) {
		t.Helper()
		req, err := prepare(cfg, args, noEnv, specs)
		if err != nil {
			t.Fatal(err)
		}
		var out, stderr bytes.Buffer
		code := execute(context.Background(), req, strings.NewReader(body), &out, &stderr)
		return code, out.String(), stderr.String()
	}
	code, out, stderr := call([]string{"-qsv", "--noproxy", "*", "--data-binary", "@-", "-H", "Authorization: placeholder", "-H", "X-Tenant: wrong", "-H", "X-Debug: true", "http://localhost:3000/users?q=a%20b"}, "payload")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	got := <-requests
	want := received{"/api/users", "q=a+b&region=eu", "POST", "Bearer secret-token", "tenant", "", "payload", got.userAgent}
	if got != want {
		t.Fatalf("request %#v, want %#v", got, want)
	}
	if got.userAgent == "curlrc-agent" {
		t.Fatal("-q failed to disable curlrc")
	}
	if out != "http://localhost:3000/users [REDACTED]" {
		t.Fatalf("output %q", out)
	}
	if strings.Contains(stderr, "secret-token") || strings.Contains(stderr, server.URL) {
		t.Fatalf("verbose leaked: %s", stderr)
	}
	code, _, _ = call([]string{"-s", "--noproxy", "*", "http://localhost:3000/users"}, "")
	if code != 0 || (<-requests).userAgent != "curlrc-agent" {
		t.Fatal("did not preserve curlrc")
	}
	code, _, _ = call([]string{"-qsf", "--noproxy", "*", "http://localhost:3000/fail"}, "")
	if code != 22 {
		t.Fatalf("failure code %d", code)
	}
	<-requests
	code, _, _ = call([]string{"-qsL", "--noproxy", "*", "http://localhost:3000/redirect"}, "")
	if code != 0 {
		t.Fatal("redirect failed")
	}
	<-requests
	if got := <-requests; got.path != "/api/landing" {
		t.Fatalf("did not follow redirect: %#v", got)
	}
	output := filepath.Join(t.TempDir(), "response.bin")
	code, out, _ = call([]string{"-qs", "--noproxy", "*", "-o", output, "-w", "%{http_code}", "http://localhost:3000/users"}, "")
	if code != 0 || out != "200" {
		t.Fatalf("output/write-out %d %q", code, out)
	}
	<-requests
	data, err := os.ReadFile(output)
	if err != nil || string(data) != server.URL+"/api/users secret-token" {
		t.Fatal("curl file output changed")
	}
	cfg.Mappings = append(cfg.Mappings, mapping{Name: "other", Alias: "http://other.local", Target: server.URL + "/other", Token: "second-token"})
	code, _, stderr = call([]string{"-qs", "--noproxy", "*", "http://localhost:3000/users", "--next", "--noproxy", "*", "http://other.local/users"}, "")
	if code != 0 {
		t.Fatalf("next: %s", stderr)
	}
	first, second := <-requests, <-requests
	if first.auth != "Bearer secret-token" || second.auth != "Bearer second-token" || second.tenant != "" {
		t.Fatalf("transfer credentials leaked: %#v %#v", first, second)
	}
	t.Setenv("OCURLA_REDACT", "0")
	code, out, _ = call([]string{"-qs", "--noproxy", "*", "http://localhost:3000/users"}, "")
	if code != 0 || out != server.URL+"/api/users secret-token" {
		t.Fatalf("raw output: %q", out)
	}
	<-requests
}
