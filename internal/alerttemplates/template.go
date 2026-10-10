package alerttemplates

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"text/template"
	"text/template/parse"
	"time"

	common "github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/promql"
	promtemplate "github.com/prometheus/prometheus/template"
	"github.com/prometheus/prometheus/util/features"
)

const MaxSteps = 50000
const prefix = "{{- $labels := .Labels -}}{{- $values := .Values -}}{{- $value := .Value -}}"
const stepName = "__mp_template_step"
const emitName = "__mp_template_emit"

type Program struct {
	original, source, name string
	err                    error
}

// Discover upstream function names through its public feature registry. Parsing
// needs names only; execution still uses the actual upstream implementations.
func knownFunctions() template.FuncMap {
	registry := features.NewRegistry()
	promtemplate.RegisterFeatures(registry)
	result := template.FuncMap{}
	placeholder := func(...any) any { return nil }
	for name := range registry.Get()[features.TemplatingFunctions] {
		result[name] = placeholder
	}
	for name := range functions() {
		result[name] = placeholder
	}
	return result
}

var parseFunctions = knownFunctions()
var errorLocation = regexp.MustCompile(`template: [^:]+:\d+(?::\d+)?:\s*`)

func Compile(name, raw string) Program {
	p := Program{original: raw, name: "__alert_" + name}
	if !strings.Contains(raw, "{{") {
		return p
	}
	t, err := template.New(p.name).Funcs(parseFunctions).Parse(prefix + raw)
	if err != nil {
		p.err = err
		return p
	}
	step, err := template.New("step").Funcs(template.FuncMap{stepName: func() string { return "" }}).Parse("{{" + stepName + "}}")
	if err != nil {
		p.err = err
		return p
	}
	var instrument func(*parse.ListNode)
	instrument = func(list *parse.ListNode) {
		if list == nil {
			return
		}
		nodes := []parse.Node{step.Tree.Root.Nodes[0].Copy()}
		for _, node := range list.Nodes {
			switch n := node.(type) {
			case *parse.TextNode:
				emit, _ := template.New("text").Funcs(template.FuncMap{emitName: func(any) string { return "" }}).Parse(fmt.Sprintf("{{%s %q}}", emitName, string(n.Text)))
				nodes = append(nodes, emit.Tree.Root.Nodes[0])
				continue
			case *parse.ActionNode:
				if len(n.Pipe.Decl) == 0 {
					n.Pipe.Cmds = append(n.Pipe.Cmds, &parse.CommandNode{NodeType: parse.NodeCommand, Args: []parse.Node{parse.NewIdentifier(emitName)}})
				}
			case *parse.IfNode:
				instrument(n.List)
				instrument(n.ElseList)
			case *parse.WithNode:
				instrument(n.List)
				instrument(n.ElseList)
			case *parse.RangeNode:
				instrument(n.List)
				instrument(n.ElseList)
			}
			nodes = append(nodes, node)
		}
		list.Nodes = nodes
	}
	templates := t.Templates()
	slices.SortFunc(templates, func(a, b *template.Template) int { return strings.Compare(a.Name(), b.Name()) })
	var source strings.Builder
	for _, part := range templates {
		instrument(part.Tree.Root)
		fmt.Fprintf(&source, "{{define %q}}%s{{end}}", part.Name(), part.Tree.Root.String())
	}
	p.source = source.String()
	return p
}

type execution struct {
	ctx                 context.Context
	steps, bytes, limit int
}

func (e *execution) step() (string, error) {
	e.steps++
	if e.steps > MaxSteps {
		return "", errors.New("template exceeds 50000 execution steps")
	}
	return "", e.ctx.Err()
}

type boundedText struct {
	strings.Builder
	remaining int
}

func (b *boundedText) Write(raw []byte) (int, error) {
	if len(raw) > b.remaining {
		return 0, errors.New("template output exceeds field limit")
	}
	b.remaining -= len(raw)
	return b.Builder.Write(raw)
}
func (e *execution) emit(value any) (string, error) {
	if _, err := e.step(); err != nil {
		return "", err
	}
	writer := &boundedText{remaining: e.limit - e.bytes}
	if value == nil {
		value = "<no value>"
	}
	if _, err := fmt.Fprint(writer, value); err != nil {
		return "", err
	}
	e.bytes += writer.Len()
	return writer.String(), nil
}

func (p Program) Expand(ctx context.Context, data Data, root *url.URL, at time.Time, limit int) (string, error) {
	if p.err != nil {
		return p.original, p.err
	}
	if p.source == "" {
		return p.original, nil
	}
	if root == nil {
		root = &url.URL{}
	}
	guard := &execution{ctx: ctx, limit: limit}
	expander := promtemplate.NewTemplateExpander(ctx, p.source, p.name, data, common.Time(at.UnixMilli()), func(context.Context, string, time.Time) (promql.Vector, error) { return nil, nil }, root, []string{"missingkey=invalid"})
	expander.Funcs(functions())
	boundedFormat := func(format string, args ...any) (string, error) {
		w := &boundedText{remaining: limit}
		_, err := fmt.Fprintf(w, format, args...)
		return w.String(), err
	}
	expander.Funcs(template.FuncMap{
		stepName: guard.step, emitName: guard.emit,
		"printf": boundedFormat,
		"print": func(args ...any) (string, error) {
			w := &boundedText{remaining: limit}
			_, err := fmt.Fprint(w, args...)
			return w.String(), err
		},
		"println": func(args ...any) (string, error) {
			w := &boundedText{remaining: limit}
			_, err := fmt.Fprintln(w, args...)
			return w.String(), err
		},
	})
	result, err := expander.Expand()
	if err != nil {
		return p.original, err
	}
	result = strings.ReplaceAll(result, "<no value>", "[no value]")
	if len(result) > limit {
		return p.original, io.ErrShortBuffer
	}
	return result, nil
}

type Set map[string]Program

func CompileSet(name string, values map[string]string) Set {
	set := Set{}
	for key, value := range values {
		set[key] = Compile(name+"_"+key, value)
	}
	return set
}
func (s Set) Expand(ctx context.Context, data Data, root *url.URL, at time.Time, limit int) (map[string]string, []string) {
	if len(s) == 0 {
		return nil, nil
	}
	result := map[string]string{}
	var errors []string
	keys := make([]string, 0, len(s))
	for key := range s {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		value, err := s[key].Expand(ctx, data, root, at, limit)
		result[key] = value
		if err != nil {
			message := key + ": " + errorLocation.ReplaceAllString(err.Error(), "")
			message = strings.ReplaceAll(message, stepName, "execution budget")
			message = strings.ReplaceAll(message, emitName, "output budget")
			if len(message) > 512 {
				message = message[:512]
			}
			errors = append(errors, message)
		}
	}
	return result, errors
}
