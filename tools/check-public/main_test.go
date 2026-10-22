package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixtureGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	values := append([]string{"-c", "user.name=public-check-fixture", "-c", "user.email=fixture@example.com", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=" + filepath.Join(repo, ".unused-test-hooks")}, args...)
	command := gitCommand(ctx, repo, values...)
	data, err := command.Output()
	if err != nil {
		t.Fatal("isolated Git fixture operation failed")
	}
	return strings.TrimSpace(string(data))
}

func writeFixture(t *testing.T, repo, name, text string) {
	t.Helper()
	file := filepath.Join(repo, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(text), 0644); err != nil {
		t.Fatal(err)
	}
}

func modulesText() string {
	var out strings.Builder
	for _, child := range children {
		out.WriteString("[submodule \"" + child + "\"]\n path = " + child + "\n url = https://github.com/neko233-com/" + child + ".git\n")
	}
	return out.String()
}

func gitFixture(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "public")
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range append([]string{""}, children...) {
		repo := filepath.Join(root, prefix)
		if err := os.MkdirAll(repo, 0755); err != nil {
			t.Fatal(err)
		}
		fixtureGit(t, repo, "init", "--initial-branch=main")
		writeFixture(t, repo, "README.md", "public fixture\n")
		fixtureGit(t, repo, "add", "README.md")
		fixtureGit(t, repo, "commit", "-m", "public fixture")
	}
	writeFixture(t, root, ".gitmodules", modulesText())
	fixtureGit(t, root, "add", ".gitmodules")
	for _, child := range children {
		head := fixtureGit(t, filepath.Join(root, child), "rev-parse", "HEAD")
		fixtureGit(t, root, "update-index", "--add", "--cacheinfo", "160000,"+head+","+child)
	}
	fixtureGit(t, root, "commit", "-m", "public child pointers")
	return root
}

func gate(t *testing.T, root string, extra ...string) (int, string) {
	t.Helper()
	var output bytes.Buffer
	code := run(append([]string{"--root", root}, extra...), &output)
	return code, output.String()
}

func TestCanonicalFourRepositoriesAndHistory(t *testing.T) {
	root := gitFixture(t)
	writeFixture(t, filepath.Join(root, children[0]), ".env.example", "METRICSPANEL_TOKEN=\n")
	for _, args := range [][]string{{}, {"--require-submodules"}, {"--require-submodules", "--history"}} {
		if code, output := gate(t, root, args...); code != 0 || output != "" {
			t.Fatal("clean canonical fixture failed: " + output)
		}
	}
}

func TestHistoricalTwoChildContractDoesNotWeakenCurrentModules(t *testing.T) {
	var legacy strings.Builder
	for _, child := range children[:2] {
		legacy.WriteString("[submodule \"" + child + "\"]\n path = " + child + "\n url = https://github.com/neko233-com/" + child + ".git\n")
	}
	for _, tc := range []struct {
		historical, valid bool
		text              string
	}{
		{true, true, legacy.String()}, {false, false, legacy.String()},
		{true, false, strings.ReplaceAll(legacy.String(), "metrics233-server.git", "unapproved.git")},
		{true, true, modulesText()}, {false, true, modulesText()},
	} {
		c := &checker{seen: make(map[finding]bool)}
		c.scan(source{path: ".gitmodules", display: ".gitmodules", parent: true, historical: tc.historical}, []byte(tc.text))
		if (len(c.findings) == 0) != tc.valid {
			t.Fatal("historical/current module contract mismatch")
		}
	}
}

func TestHistoricalThreeChildContractAndClientLanguageSourceScanning(t *testing.T) {
	var legacy strings.Builder
	for _, child := range children[:3] {
		legacy.WriteString("[submodule \"" + child + "\"]\n path = " + child + "\n url = https://github.com/neko233-com/" + child + ".git\n")
	}
	for _, historical := range []bool{false, true} {
		c := &checker{seen: make(map[finding]bool)}
		c.scan(source{path: ".gitmodules", display: ".gitmodules", parent: true, historical: historical}, []byte(legacy.String()))
		if (len(c.findings) == 0) != historical {
			t.Fatal("three-module history must not weaken the current six-module contract")
		}
	}
	for _, file := range []string{"Client.cs", "Consumer.mjs", "Consumer.cjs", "Client.csproj"} {
		c := &checker{seen: make(map[finding]bool)}
		c.scan(source{path: file, display: file}, []byte("https://192.168.1.2/private"))
		if len(c.findings) == 0 {
			t.Fatal("client language private URL not scanned: " + file)
		}
	}
}

func TestStagedSecretIsDetectedAfterWorkingCopyWasCleaned(t *testing.T) {
	root := gitFixture(t)
	repo := filepath.Join(root, children[0])
	secret := "gh" + "p_" + strings.Repeat("a", 36)
	writeFixture(t, repo, "settings.go", "package fixture\nvar value = \""+secret+"\"\n")
	fixtureGit(t, repo, "add", "settings.go")
	writeFixture(t, repo, "settings.go", "package fixture\n")
	code, output := gate(t, root)
	if code == 0 || !strings.Contains(output, "settings.go:2:github-token") || strings.Contains(output, secret) {
		t.Fatal("staged-only secret escaped or leaked into output")
	}
	if data, _ := os.ReadFile(filepath.Join(repo, "settings.go")); string(data) != "package fixture\n" {
		t.Fatal("scanner modified a working file")
	}
}

func TestPrivateFileAndDeployPathsIncludeTrackedIgnoredFiles(t *testing.T) {
	root := gitFixture(t)
	repo := filepath.Join(root, children[1])
	writeFixture(t, repo, ".gitignore", ".env*\n*.db\n")
	writeFixture(t, repo, ".env", "ignored and private\n")
	writeFixture(t, repo, "control.db", "private fixture\n")
	fixtureGit(t, repo, "add", "-f", ".env", "control.db")
	writeFixture(t, repo, "deploy-metrics233-server/settings.txt", "private fixture\n")
	writeFixture(t, repo, "secrets.key", "private fixture\n")
	code, output := gate(t, root)
	if code == 0 || !strings.Contains(output, ".env:0:private-file") || !strings.Contains(output, "control.db:0:private-file") || !strings.Contains(output, "secrets.key:0:private-file") || !strings.Contains(output, "private-deploy-path") {
		t.Fatal("tracked/ignored/private path coverage failed")
	}
}

func TestHistoryFindsRemovedSecretsAndCommitMessages(t *testing.T) {
	root := gitFixture(t)
	repo := filepath.Join(root, children[0])
	secret := "github_" + "pat_" + strings.Repeat("a", 82)
	writeFixture(t, repo, "removed.md", secret+"\n")
	fixtureGit(t, repo, "add", "removed.md")
	fixtureGit(t, repo, "commit", "-m", "historical fixture")
	fixtureGit(t, repo, "rm", "removed.md")
	fixtureGit(t, repo, "commit", "-m", "remove historical fixture")
	if code, output := gate(t, root); code != 0 || output != "" {
		t.Fatal("working/index check unexpectedly inspected history")
	}
	code, output := gate(t, root, "--history")
	if code == 0 || !strings.Contains(output, "removed.md@main-") || !strings.Contains(output, "github-fine-grained-token") || strings.Contains(output, secret) {
		t.Fatal("reachable removed secret escaped or was printed")
	}
	fixtureGit(t, filepath.Join(root, children[1]), "commit", "--allow-empty", "-m", secret)
	code, output = gate(t, root, "--history")
	if code == 0 || !strings.Contains(output, "/[commit]") || strings.Contains(output, secret) {
		t.Fatal("history commit metadata escaped or was printed")
	}
}

func TestSyntheticTestIPIsExplicitAndCannotBypassDeny(t *testing.T) {
	root := gitFixture(t)
	repo := filepath.Join(root, children[0])
	address := "http://" + "10.23.45.67/service"
	writeFixture(t, repo, "fixture_test.go", "// "+address+"\n")
	if code, output := gate(t, root); code == 0 || !strings.Contains(output, "rfc1918-http-url") || strings.Contains(output, address) {
		t.Fatal("private HTTP URL was not rejected without explicit opt-in")
	}
	if code, output := gate(t, root, "--allow-synthetic-test-ips"); code != 0 || output != "" {
		t.Fatal("explicit test-only RFC1918 opt-in failed")
	}
	deny := filepath.Join(filepath.Dir(root), "private-deny.txt")
	if err := os.WriteFile(deny, []byte("10.23.45.67\n"), 0600); err != nil {
		t.Fatal(err)
	}
	code, output := gate(t, root, "--allow-synthetic-test-ips", "--deny-file", deny)
	if code == 0 || !strings.Contains(output, "deny#1") || strings.Contains(output, "10.23.45.67") {
		t.Fatal("test-only exception bypassed private deny list or leaked its value")
	}
	writeFixture(t, repo, "config.go", "// "+address+"\n")
	if code, output := gate(t, root, "--allow-synthetic-test-ips"); code == 0 || !strings.Contains(output, "config.go:1:rfc1918-http-url") {
		t.Fatal("test exception leaked into ordinary production source")
	}
}

func TestExternalDenyIsCaseInsensitiveAndNeverPrintedInPaths(t *testing.T) {
	root := gitFixture(t)
	deny := filepath.Join(filepath.Dir(root), "private-deny.txt")
	value := "blocked.example.invalid"
	if err := os.WriteFile(deny, []byte("\ufeff# private input\n"+value+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, root, "docs/"+value+".md", strings.ToUpper(value)+"\n")
	code, output := gate(t, root, "--deny-file", deny)
	if code == 0 || strings.Contains(strings.ToLower(output), value) || !strings.Contains(output, "[deny#1].md") || !strings.Contains(output, ":1:deny#1") {
		t.Fatal("deny list did not match/redact path and content")
	}
	inside := filepath.Join(root, "deny.txt")
	writeFixture(t, root, "deny.txt", value)
	if code, output := gate(t, root, "--deny-file", inside); code == 0 || !strings.Contains(output, "deny-file-must-be-external") || strings.Contains(output, value) {
		t.Fatal("public-tree deny configuration was accepted")
	}
}

func TestGitmodulesAndPinnedCheckoutsFailClosed(t *testing.T) {
	root := gitFixture(t)
	bad := strings.Replace(modulesText(), "https://github.com/", "https://example.invalid/", 1)
	writeFixture(t, root, ".gitmodules", bad)
	if code, output := gate(t, root); code == 0 || !strings.Contains(output, "gitmodules-not-canonical") {
		t.Fatal("noncanonical public submodule URL accepted")
	}
	writeFixture(t, root, ".gitmodules", modulesText())
	fixtureGit(t, filepath.Join(root, children[0]), "commit", "--allow-empty", "-m", "unpinned fixture")
	if code, output := gate(t, root, "--require-submodules"); code == 0 || !strings.Contains(output, "gitlink-checkout-mismatch") {
		t.Fatal("mismatched checkout accepted")
	}
	fixtureGit(t, root, "update-index", "--force-remove", children[1])
	if code, output := gate(t, root, "--require-submodules"); code == 0 || !strings.Contains(output, "gitlink-required") {
		t.Fatal("missing required gitlink accepted")
	}
}

func TestPatternRulesAndPathBoundaries(t *testing.T) {
	c := &checker{seen: make(map[finding]bool)}
	data := strings.Join([]string{
		"gh" + "o_" + strings.Repeat("a", 36),
		"AK" + "IA" + strings.Repeat("A", 16),
		"aws_secret_access_key=" + strings.Repeat("B", 40),
		"-----BEGIN " + "PRIVATE KEY-----",
	}, "\n")
	c.scan(source{path: "fixture.go", display: "fixture.go"}, []byte(data))
	if len(c.findings) != 4 {
		t.Fatal("high-confidence pattern coverage failed")
	}
	for _, private := range []string{"secrets.key", "local.sqlite3", "id_ed25519", ".env.local", ".ENV.EXAMPLE", "private-deploy/config.txt"} {
		if c.pathRules(source{path: private, display: private}) {
			t.Fatal("private path was not rejected")
		}
	}
	for _, public := range []string{".env.example", "id_ed25519.pub", "docs/deployment.md", "internal/key.go"} {
		if !c.pathRules(source{path: public, display: public}) {
			t.Fatal("public path was rejected")
		}
	}
	for _, unsafe := range []string{"../escape", "/absolute", "a/../escape", `a\escape`, "C:/absolute"} {
		if validRelative(unsafe) {
			t.Fatal("unsafe path accepted")
		}
	}
	for _, address := range []string{"http://127.0.0.1:7333", "http://192.0.2.1", "https://metrics.example.com"} {
		if privateHTTP([]byte(address)) {
			t.Fatal("public/loopback documentation URL was rejected")
		}
	}
}

func TestMissingGitAndMalformedOptionsDoNotEchoValues(t *testing.T) {
	root := t.TempDir()
	if code, output := gate(t, root); code == 0 || !strings.Contains(output, "git-worktree-unavailable") {
		t.Fatal("non-Git trees were silently accepted")
	}
	var output bytes.Buffer
	value := "should-never-be-printed.example.invalid"
	if run([]string{"--unknown=" + value}, &output) == 0 || strings.Contains(output.String(), value) {
		t.Fatal("malformed option leaked its supplied value")
	}
}

func TestGitmodulesRejectDuplicateAndExtraDirectives(t *testing.T) {
	if !validGitmodules([]byte(modulesText())) {
		t.Fatal("canonical modules rejected")
	}
	for _, text := range []string{
		modulesText() + " branch = main\n",
		strings.Replace(modulesText(), " path = ", " path =\n path = ", 1),
		modulesText() + "[include]\n path = other\n",
	} {
		if validGitmodules([]byte(text)) {
			t.Fatal("ambiguous/extra .gitmodules directive accepted")
		}
	}
}

func TestHistoryDetachedMainFallbackAndEncodingFailClosed(t *testing.T) {
	root := gitFixture(t)
	repo := filepath.Join(root, children[0])
	head := fixtureGit(t, repo, "rev-parse", "HEAD")
	fixtureGit(t, repo, "update-ref", "refs/remotes/origin/main", head)
	fixtureGit(t, repo, "checkout", "--detach", head)
	fixtureGit(t, repo, "branch", "-D", "main")
	if code, output := gate(t, root, "--history"); code != 0 || output != "" {
		t.Fatal("detached checkout origin/main fallback failed: " + output)
	}
	c := &checker{seen: make(map[finding]bool)}
	c.scan(source{path: "settings.ps1", display: "settings.ps1"}, []byte{0xff, 0xfe, 'a', 0})
	if len(c.findings) == 0 || c.findings[0].Rule != "invalid-text-encoding" {
		t.Fatal("unscannable text encoding did not fail closed")
	}
}
