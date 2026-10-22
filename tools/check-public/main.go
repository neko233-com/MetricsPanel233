// check-public 在发布前只读检查父仓库及全部正式公共子模块工作树。
// 输出仅包含路径、行号和规则；不得输出匹配值、Git stderr 或私有 deny 配置。
package main

import (
	"bufio"
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const maxFileBytes = 32 * 1024 * 1024

var children = []string{"metrics233-server", "metrics233-lib-go", "metrics-lib-rust", "metrics233-client-sdk-typescript", "metrics233-client-sdk-csharp", "metrics233-client-sdk-rust"}

func canonicalChild(name string) bool {
	for _, child := range children {
		if name == child {
			return true
		}
	}
	return false
}

type options struct {
	root, denyFile                           string
	history, requireSubmodules, allowTestIPs bool
}

type finding struct {
	Path string
	Line int
	Rule string
}

type denyRule struct {
	pattern *regexp.Regexp
	name    string
}

type checker struct {
	options
	root     string
	findings []finding
	seen     map[finding]bool
	denies   []denyRule
}

type source struct {
	path, display string
	parent        bool
	historical    bool
}

type object struct {
	kind    string
	sources []source
}

type entry struct {
	mode, oid, path string
}

type patternRule struct {
	name    string
	pattern *regexp.Regexp
}

var patterns = []patternRule{
	{"github-token", regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,255}\b`)},
	{"github-fine-grained-token", regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{60,255}\b`)},
	{"aws-access-key", regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`)},
	{"aws-secret-key", regexp.MustCompile(`(?i)\baws[_-]?secret[_-]?access[_-]?key\b["']?\s*[:=]\s*["']?[A-Za-z0-9/+=]{40}(?:["'\s]|$)`)},
	{"private-key", regexp.MustCompile(`-----BEGIN (?:RSA |EC |DSA |OPENSSH |ENCRYPTED )?PRIVATE KEY-----`)},
}

var httpURL = regexp.MustCompile(`(?i)https?://[^\s"'<>]+`)
var objectID = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)

func main() { os.Exit(run(os.Args[1:], os.Stdout)) }

func run(args []string, output io.Writer) int {
	var opts options
	flags := flag.NewFlagSet("check-public", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&opts.root, "root", ".", "parent checkout")
	flags.StringVar(&opts.denyFile, "deny-file", "", "external UTF-8 deny list")
	flags.BoolVar(&opts.history, "history", false, "scan all reachable main history")
	flags.BoolVar(&opts.requireSubmodules, "require-submodules", false, "require canonical gitlinks and matching checkouts")
	flags.BoolVar(&opts.allowTestIPs, "allow-synthetic-test-ips", false, "allow RFC1918 URL fixtures only in *_test.go/testdata")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		if err == flag.ErrHelp {
			fmt.Fprintln(output, "go run ./tools/check-public/main.go --root . [--deny-file EXTERNAL_FILE] [--history] [--require-submodules] [--allow-synthetic-test-ips]")
			return 0
		}
		fmt.Fprintln(output, ".:0:invalid-options")
		return 1
	}
	c := &checker{options: opts, seen: make(map[finding]bool)}
	c.check()
	sort.Slice(c.findings, func(i, j int) bool {
		a, b := c.findings[i], c.findings[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Rule < b.Rule
	})
	for _, issue := range c.findings {
		fmt.Fprintf(output, "%s:%d:%s\n", c.displayPath(issue.Path), issue.Line, issue.Rule)
	}
	if len(c.findings) != 0 {
		return 1
	}
	return 0
}

func (c *checker) add(file string, line int, rule string) {
	issue := finding{Path: file, Line: line, Rule: rule}
	if !c.seen[issue] {
		c.seen[issue] = true
		c.findings = append(c.findings, issue)
	}
}

func (c *checker) displayPath(file string) string {
	for _, rule := range c.denies {
		file = rule.pattern.ReplaceAllLiteralString(file, "["+rule.name+"]")
	}
	for _, rule := range patterns {
		file = rule.pattern.ReplaceAllLiteralString(file, "["+rule.name+"]")
	}
	// Quoting control characters prevents attacker-controlled filenames from
	// injecting additional report records. Ordinary repository paths stay plain.
	if strings.ContainsAny(file, "\r\n\t:") || !utf8.ValidString(file) {
		return strconv.QuoteToASCII(file)
	}
	return file
}

func within(root, file string) bool {
	relative, err := filepath.Rel(root, file)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func samePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func (c *checker) check() {
	absolute, err := filepath.Abs(c.options.root)
	if err != nil {
		c.add(".", 0, "root-unavailable")
		return
	}
	c.root, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		c.add(".", 0, "root-unavailable")
		return
	}
	if !c.loadDeny() {
		return
	}
	for _, prefix := range append([]string{""}, children...) {
		repo := filepath.Join(c.root, prefix)
		display := prefix
		if display == "" {
			display = "."
		}
		resolved, err := filepath.EvalSymlinks(repo)
		if err != nil || !samePath(resolved, repo) {
			c.add(display, 0, "git-worktree-unavailable")
			continue
		}
		top, ok := gitRead(repo, "rev-parse", "--show-toplevel")
		if !ok || !samePath(strings.TrimSpace(string(top)), repo) {
			c.add(display, 0, "git-worktree-unavailable")
			continue
		}
		c.repository(repo, prefix)
	}
}

func (c *checker) loadDeny() bool {
	if c.denyFile == "" {
		return true
	}
	file, err := filepath.Abs(c.denyFile)
	if err == nil {
		file, err = filepath.EvalSymlinks(file)
	}
	if err != nil {
		c.add("[deny-file]", 0, "deny-file-unavailable")
		return false
	}
	if within(c.root, file) {
		c.add("[deny-file]", 0, "deny-file-must-be-external")
		return false
	}
	stream, err := os.Open(file)
	if err != nil {
		c.add("[deny-file]", 0, "deny-file-unavailable")
		return false
	}
	defer stream.Close()
	data, err := io.ReadAll(io.LimitReader(stream, 1024*1024+1))
	if err != nil || len(data) > 1024*1024 || !utf8.Valid(data) {
		c.add("[deny-file]", 0, "deny-file-invalid")
		return false
	}
	text := strings.TrimPrefix(string(data), "\ufeff")
	for _, line := range strings.Split(text, "\n") {
		value := strings.TrimSpace(line)
		if value == "" || strings.HasPrefix(value, "#") {
			continue
		}
		if strings.IndexByte(value, 0) >= 0 || len(value) > 4096 {
			c.add("[deny-file]", 0, "deny-file-invalid")
			return false
		}
		if len(c.denies) >= 1024 {
			c.add("[deny-file]", 0, "deny-file-invalid")
			return false
		}
		name := "deny#" + strconv.Itoa(len(c.denies)+1)
		c.denies = append(c.denies, denyRule{pattern: regexp.MustCompile("(?i)" + regexp.QuoteMeta(value)), name: name})
	}
	if len(c.denies) == 0 {
		c.add("[deny-file]", 0, "deny-file-empty")
		return false
	}
	return true
}

func gitCommand(ctx context.Context, repo string, args ...string) *exec.Cmd {
	values := append([]string{"--no-replace-objects", "--no-lazy-fetch", "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false", "-c", "maintenance.auto=false", "-C", repo}, args...)
	command := exec.CommandContext(ctx, "git", values...)
	for _, item := range os.Environ() {
		name, _, _ := strings.Cut(item, "=")
		switch strings.ToUpper(name) {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES":
			continue
		}
		command.Env = append(command.Env, item)
	}
	command.Env = append(command.Env, "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "GIT_NO_LAZY_FETCH=1")
	command.Stderr = io.Discard
	return command
}

func gitRead(repo string, args ...string) ([]byte, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	data, err := gitCommand(ctx, repo, args...).Output()
	return data, err == nil
}

func parseEntries(data []byte, tree bool) ([]entry, bool) {
	var out []entry
	for _, record := range bytes.Split(data, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		metadata, file, found := bytes.Cut(record, []byte{'\t'})
		fields := strings.Fields(string(metadata))
		if !found || len(fields) != 3 {
			return nil, false
		}
		oid, stage := fields[1], fields[2]
		if tree {
			oid, stage = fields[2], "0"
		}
		if stage != "0" || !objectID.MatchString(oid) || len(file) == 0 {
			return nil, false
		}
		out = append(out, entry{mode: fields[0], oid: oid, path: string(file)})
	}
	return out, true
}

func validRelative(file string) bool {
	return file != "" && !strings.Contains(file, "\\") && !strings.Contains(file, ":") && !strings.HasPrefix(file, "/") && path.Clean(file) == file && file != ".." && !strings.HasPrefix(file, "../")
}

func parentChild(file string) bool {
	for _, child := range children {
		if file == child || strings.HasPrefix(file, child+"/") {
			return true
		}
	}
	return false
}

func (c *checker) pathRules(src source) bool {
	if !validRelative(src.path) {
		c.add(src.display, 0, "unsafe-path")
		return false
	}
	for _, rule := range c.denies {
		if rule.pattern.MatchString(src.path) {
			c.add(src.display, 0, rule.name)
		}
	}
	base := path.Base(src.path)
	lower := strings.ToLower(base)
	private := base != ".env.example" && (lower == ".env" || strings.HasPrefix(lower, ".env."))
	for _, suffix := range []string{".key", ".db", ".db-wal", ".db-shm", ".sqlite", ".sqlite3", ".sqlite-wal", ".sqlite-shm", ".sqlite3-wal", ".sqlite3-shm", ".p12", ".pfx", ".jks"} {
		private = private || strings.HasSuffix(lower, suffix)
	}
	for _, name := range []string{"id_rsa", "id_dsa", "id_ecdsa", "id_ed25519"} {
		private = private || lower == name
	}
	for _, part := range strings.Split(strings.ToLower(src.path), "/") {
		if part == "deploy-metrics233-server" || part == "private-deploy" || part == ".secrets" {
			c.add(src.display, 0, "private-deploy-path")
			return false
		}
	}
	if private {
		c.add(src.display, 0, "private-file")
		return false
	}
	return true
}

func (c *checker) repository(repo, prefix string) {
	display := prefix
	if display == "" {
		display = "."
	}
	data, ok := gitRead(repo, "ls-files", "--stage", "-z")
	if !ok {
		c.add(display, 0, "git-index-unavailable")
		return
	}
	entries, ok := parseEntries(data, false)
	if !ok {
		c.add(display, 0, "git-index-invalid-or-unmerged")
		return
	}
	objects := make(map[string]*object)
	files := make(map[string]bool)
	links := make(map[string]string)
	for _, item := range entries {
		if item.mode == "160000" {
			if prefix == "" && canonicalChild(item.path) {
				links[item.path] = item.oid
			} else {
				c.add(path.Join(prefix, item.path), 0, "unexpected-gitlink")
			}
			continue
		}
		if prefix == "" && parentChild(item.path) {
			c.add(item.path, 0, "child-files-tracked-by-parent")
		}
		files[item.path] = true
		src := source{path: item.path, display: path.Join(prefix, item.path), parent: prefix == ""}
		if !c.pathRules(src) {
			continue
		}
		if item.mode != "100644" && item.mode != "100755" {
			c.add(src.display, 0, "non-regular-file")
			continue
		}
		appendObject(objects, item.oid, "blob", src)
	}
	untracked, ok := gitRead(repo, "ls-files", "--others", "--exclude-standard", "-z")
	if !ok {
		c.add(display, 0, "git-untracked-unavailable")
		return
	}
	for _, file := range bytes.Split(untracked, []byte{0}) {
		if len(file) != 0 {
			files[string(file)] = true
		}
	}
	for file := range files {
		if prefix == "" && parentChild(strings.TrimSuffix(file, "/")) {
			continue // Each child is inventoried independently through its own index.
		}
		src := source{path: file, display: path.Join(prefix, file), parent: prefix == ""}
		if !c.pathRules(src) {
			continue
		}
		absolute := filepath.Join(repo, filepath.FromSlash(file))
		info, err := os.Lstat(absolute)
		if os.IsNotExist(err) {
			continue // The staged version is still scanned before a deletion is staged.
		}
		if err != nil {
			c.add(src.display, 0, "file-unavailable")
			continue
		}
		if !info.Mode().IsRegular() {
			c.add(src.display, 0, "non-regular-file")
			continue
		}
		resolved, err := filepath.EvalSymlinks(absolute)
		if err != nil || !within(repo, resolved) {
			c.add(src.display, 0, "unsafe-path")
			continue
		}
		stream, err := os.Open(absolute)
		if err != nil {
			c.add(src.display, 0, "file-unavailable")
			continue
		}
		c.scanReader(src, stream)
		stream.Close()
	}
	modules := filepath.Join(repo, ".gitmodules")
	if _, err := os.Stat(modules); err == nil {
		if !files[".gitmodules"] {
			c.add(path.Join(prefix, ".gitmodules"), 0, "gitmodules-not-in-publish-set")
		}
	} else if prefix == "" && c.requireSubmodules {
		c.add(".gitmodules", 0, "gitmodules-required")
	}
	if prefix == "" && c.requireSubmodules {
		for _, child := range children {
			if links[child] == "" {
				c.add(child, 0, "gitlink-required")
				continue
			}
			head, ok := gitRead(filepath.Join(repo, child), "rev-parse", "--verify", "HEAD")
			if !ok || strings.TrimSpace(string(head)) != links[child] {
				c.add(child, 0, "gitlink-checkout-mismatch")
			}
		}
	}
	if c.history {
		c.historyObjects(repo, prefix, objects)
	}
	c.scanObjects(repo, display, objects)
}

func appendObject(objects map[string]*object, oid, kind string, src source) {
	item := objects[oid]
	if item == nil {
		item = &object{kind: kind}
		objects[oid] = item
	}
	for _, previous := range item.sources {
		if previous == src {
			return
		}
	}
	item.sources = append(item.sources, src)
}

func (c *checker) historyObjects(repo, prefix string, objects map[string]*object) {
	display := prefix
	if display == "" {
		display = "."
	}
	shallow, ok := gitRead(repo, "rev-parse", "--is-shallow-repository")
	if !ok || strings.TrimSpace(string(shallow)) != "false" {
		c.add(display, 0, "history-incomplete")
		return
	}
	grafts, ok := gitRead(repo, "rev-parse", "--git-path", "info/grafts")
	if !ok {
		c.add(display, 0, "history-incomplete")
		return
	}
	graftPath := strings.TrimSpace(string(grafts))
	if !filepath.IsAbs(graftPath) {
		graftPath = filepath.Join(repo, graftPath)
	}
	if info, err := os.Stat(graftPath); err == nil && info.Size() != 0 || err != nil && !os.IsNotExist(err) {
		c.add(display, 0, "history-grafts-not-supported")
		return
	}
	mainRef := "refs/heads/main"
	if _, ok := gitRead(repo, "rev-parse", "--verify", "--quiet", mainRef+"^{commit}"); !ok {
		mainRef = "refs/remotes/origin/main"
	}
	commits, ok := gitRead(repo, "rev-list", mainRef)
	if !ok || len(bytes.TrimSpace(commits)) == 0 {
		c.add(display, 0, "main-history-unavailable")
		return
	}
	seen := make(map[string]bool)
	for _, oid := range strings.Fields(string(commits)) {
		if !objectID.MatchString(oid) {
			c.add(display, 0, "main-history-invalid")
			return
		}
		version := "@main-" + oid[:12]
		appendObject(objects, oid, "commit", source{path: "[commit]", display: display + version + "/[commit]"})
		data, ok := gitRead(repo, "ls-tree", "-r", "-z", "--full-tree", oid)
		if !ok {
			c.add(display, 0, "history-tree-unavailable")
			return
		}
		entries, ok := parseEntries(data, true)
		if !ok {
			c.add(display, 0, "history-tree-invalid")
			return
		}
		for _, item := range entries {
			src := source{path: item.path, display: path.Join(prefix, item.path) + version, parent: prefix == "", historical: true}
			if item.mode == "160000" {
				if prefix != "" || !canonicalChild(item.path) {
					c.add(src.display, 0, "unexpected-gitlink")
				}
				continue
			}
			if seen[item.oid+"\x00"+item.path] {
				continue
			}
			seen[item.oid+"\x00"+item.path] = true
			if !c.pathRules(src) {
				continue
			}
			if item.mode != "100644" && item.mode != "100755" {
				c.add(src.display, 0, "non-regular-file")
				continue
			}
			appendObject(objects, item.oid, "blob", src)
		}
	}
}

func (c *checker) scanObjects(repo, display string, objects map[string]*object) {
	if len(objects) == 0 {
		return
	}
	ids := make([]string, 0, len(objects))
	for oid := range objects {
		ids = append(ids, oid)
	}
	sort.Strings(ids)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := gitCommand(ctx, repo, "cat-file", "--batch")
	command.Stdin = strings.NewReader(strings.Join(ids, "\n") + "\n")
	stdout, err := command.StdoutPipe()
	if err != nil || command.Start() != nil {
		c.add(display, 0, "git-object-read-failed")
		return
	}
	reader := bufio.NewReader(stdout)
	valid := true
	for _, oid := range ids {
		header, err := reader.ReadString('\n')
		fields := strings.Fields(header)
		if err != nil || len(fields) != 3 || fields[0] != oid || fields[1] != objects[oid].kind {
			valid = false
			break
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || size < 0 {
			valid = false
			break
		}
		limited := &io.LimitedReader{R: reader, N: size}
		data, err := io.ReadAll(io.LimitReader(limited, maxFileBytes+1))
		if err != nil {
			valid = false
			break
		}
		for _, src := range objects[oid].sources {
			if size > maxFileBytes {
				c.add(src.display, 0, "file-exceeds-scan-limit")
			} else {
				c.scan(src, data)
			}
		}
		if _, err = io.Copy(io.Discard, limited); err != nil || limited.N != 0 {
			valid = false
			break
		}
		if tail, err := reader.ReadByte(); err != nil || tail != '\n' {
			valid = false
			break
		}
	}
	if !valid {
		cancel()
	}
	if err := command.Wait(); err != nil || !valid {
		c.add(display, 0, "git-object-read-failed")
	}
}

func (c *checker) scanReader(src source, reader io.Reader) {
	data, err := io.ReadAll(io.LimitReader(reader, maxFileBytes+1))
	if err != nil {
		c.add(src.display, 0, "file-unavailable")
		return
	}
	if len(data) > maxFileBytes {
		c.add(src.display, 0, "file-exceeds-scan-limit")
		return
	}
	c.scan(src, data)
}

func syntheticTest(file string) bool {
	return strings.HasSuffix(file, "_test.go") || strings.Contains("/"+file+"/", "/testdata/")
}

func privateHTTP(line []byte) bool {
	for _, match := range httpURL.FindAll(line, -1) {
		parsed, err := url.Parse(strings.TrimRight(string(match), ".,;)}"))
		if err != nil {
			continue
		}
		address, err := netip.ParseAddr(parsed.Hostname())
		if err == nil && address.Unmap().Is4() && address.Unmap().IsPrivate() {
			return true
		}
	}
	return false
}

func (c *checker) scan(src source, data []byte) {
	extension := strings.ToLower(path.Ext(src.path))
	text := src.path == ".gitmodules" || strings.HasPrefix(path.Base(src.path), ".env")
	for _, candidate := range []string{".go", ".md", ".json", ".yml", ".yaml", ".toml", ".ini", ".conf", ".sh", ".ps1", ".cmd", ".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".cs", ".csproj", ".html", ".css", ".svg", ".txt", ".lock"} {
		text = text || extension == candidate
	}
	if text && (!utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0) {
		c.add(src.display, 0, "invalid-text-encoding")
	}
	validModules := validGitmodules(data)
	// Only the exact earlier two/three-child public compositions are accepted
	// in history. The current worktree/index must contain all six children.
	if src.historical && !validModules {
		validModules = validGitmodulesFor(data, children[:2]) || validGitmodulesFor(data, children[:3])
	}
	if src.path == ".gitmodules" && (!src.parent || !validModules) {
		c.add(src.display, 0, "gitmodules-not-canonical")
	}
	for index, line := range bytes.Split(data, []byte{'\n'}) {
		for _, rule := range c.denies {
			if rule.pattern.Match(line) {
				c.add(src.display, index+1, rule.name)
			}
		}
		for _, rule := range patterns {
			if rule.pattern.Match(line) {
				c.add(src.display, index+1, rule.name)
			}
		}
		if privateHTTP(line) && !(c.allowTestIPs && syntheticTest(src.path)) {
			c.add(src.display, index+1, "rfc1918-http-url")
		}
	}
}

func validGitmodules(data []byte) bool {
	return validGitmodulesFor(data, children)
}

func validGitmodulesFor(data []byte, required []string) bool {
	if !utf8.Valid(data) || len(data) > 64*1024 {
		return false
	}
	values := make(map[string]map[string]string)
	section := ""
	for _, raw := range strings.Split(strings.TrimPrefix(string(data), "\ufeff"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			section = ""
			for _, child := range required {
				if line == `[submodule "`+child+`"]` && values[child] == nil {
					section, values[child] = child, make(map[string]string)
				}
			}
			if section == "" {
				return false
			}
			continue
		}
		key, value, found := strings.Cut(line, "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if section == "" || !found || (key != "path" && key != "url") {
			return false
		}
		if _, duplicate := values[section][key]; duplicate {
			return false
		}
		if strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) {
			value = value[1 : len(value)-1]
		}
		values[section][key] = value
	}
	for _, child := range required {
		if len(values[child]) != 2 || values[child]["path"] != child || values[child]["url"] != "https://github.com/neko233-com/"+child+".git" {
			return false
		}
	}
	return len(values) == len(required)
}
