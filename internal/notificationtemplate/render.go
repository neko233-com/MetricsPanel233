package notificationtemplate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	htmltemplate "html/template"
	"io"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	texttemplate "text/template"
	"text/template/parse"

	amtemplate "github.com/prometheus/alertmanager/template"
)

const MaxOutput = 128 * 1024
const MaxSteps = 10000
const guardName = "__metricspanel_step"

var ErrLimit = errors.New("notification template execution limit exceeded")

type Definition struct {
	Name     string `json:"name"`
	Template string `json:"template"`
	Version  int    `json:"version,omitempty"`
}
type Bundle struct {
	Title     string       `json:"title"`
	Message   string       `json:"message"`
	Templates []Definition `json:"templates"`
}
type Compiled struct {
	base  *texttemplate.Template
	Names map[string][]string
}

func Compile(definitions []Definition) (*Compiled, error) {
	var base *texttemplate.Template
	upstream, err := amtemplate.FromGlobs(nil, func(text *texttemplate.Template, _ *htmltemplate.Template) { base = text })
	if err != nil {
		return nil, err
	}
	// Keep default.title/message names useful for imported contact expressions;
	// the default webhook title/message remain the caller's original payload.
	if err := upstream.Parse(strings.NewReader(`{{define "default.title"}}{{.Status | toUpper}}: {{len .Alerts}} {{.CommonLabels.alertname}}{{end}}{{define "default.message"}}{{range .Alerts}}{{.Status}} {{.Labels.alertname}}{{with .Annotations.summary}}: {{.}}{{end}}{{"\n"}}{{end}}{{end}}`)); err != nil {
		return nil, err
	}
	names := map[string][]string{}
	owners := map[string]string{}
	for _, t := range base.Templates() {
		owners[t.Name()] = "built-in"
	}
	total := 0
	for _, definition := range definitions {
		total += len(definition.Template)
		if total > 1048576 || len(definitions) > 100 {
			return nil, errors.New("notification templates exceed 100 groups or 1 MiB total source")
		}
		if len(definition.Name) == 0 || len(definition.Name) > 128 || len(definition.Template) > 65536 || strings.HasPrefix(definition.Name, "__metricspanel_") {
			return nil, errors.New("invalid notification template name or source size")
		}
		local, err := texttemplate.New("__metricspanel_input").Funcs(texttemplate.FuncMap(amtemplate.DefaultFuncs)).Parse(definition.Template)
		if err != nil {
			return nil, err
		}
		custom := []string{}
		for _, t := range local.Templates() {
			if t.Name() != "__metricspanel_input" {
				custom = append(custom, t.Name())
			}
		}
		source := definition.Template
		if len(custom) == 0 {
			custom = []string{definition.Name}
			source = "{{define " + strconv.Quote(definition.Name) + "}}" + source + "{{end}}"
		} else if strings.TrimSpace(local.Tree.Root.String()) != "" {
			return nil, errors.New("notification template definitions cannot include text outside define blocks")
		}
		sort.Strings(custom)
		for _, name := range custom {
			if strings.HasPrefix(name, "__metricspanel_") || name == "" {
				return nil, errors.New("reserved notification template name")
			}
			if owner, exists := owners[name]; exists {
				return nil, fmt.Errorf("notification definition %q already belongs to %s", name, owner)
			}
			owners[name] = definition.Name
		}
		if err := upstream.Parse(strings.NewReader(source)); err != nil {
			return nil, err
		}
		names[definition.Name] = custom
	}
	if err := validateTrees(base); err != nil {
		return nil, err
	}
	return &Compiled{base: base, Names: names}, nil
}

func templateCalls(list *parse.ListNode, depth int, calls *[]string, nodes *int) error {
	if list == nil {
		return nil
	}
	if depth > 8 {
		return errors.New("notification template control nesting exceeds 8 levels")
	}
	for _, node := range list.Nodes {
		*nodes++
		if *nodes > 20000 {
			return errors.New("notification template exceeds 20000 syntax nodes")
		}
		switch n := node.(type) {
		case *parse.TemplateNode:
			*calls = append(*calls, n.Name)
		case *parse.RangeNode:
			if err := templateCalls(n.List, depth+1, calls, nodes); err != nil {
				return err
			}
			if err := templateCalls(n.ElseList, depth+1, calls, nodes); err != nil {
				return err
			}
		case *parse.IfNode:
			if err := templateCalls(n.List, depth+1, calls, nodes); err != nil {
				return err
			}
			if err := templateCalls(n.ElseList, depth+1, calls, nodes); err != nil {
				return err
			}
		case *parse.WithNode:
			if err := templateCalls(n.List, depth+1, calls, nodes); err != nil {
				return err
			}
			if err := templateCalls(n.ElseList, depth+1, calls, nodes); err != nil {
				return err
			}
		}
	}
	return nil
}
func validateTrees(base *texttemplate.Template) error {
	graph := map[string][]string{}
	nodes := 0
	for _, t := range base.Templates() {
		if t.Tree == nil {
			continue
		}
		calls := []string{}
		if err := templateCalls(t.Tree.Root, 0, &calls, &nodes); err != nil {
			return err
		}
		graph[t.Name()] = calls
	}
	states, heights := map[string]int{}, map[string]int{}
	var visit func(string, int) (int, error)
	visit = func(name string, depth int) (int, error) {
		if depth > 32 {
			return 0, errors.New("notification template call depth exceeds 32")
		}
		if states[name] == 2 {
			return heights[name], nil
		}
		if states[name] == 1 {
			return 0, errors.New("notification template recursive calls are not supported")
		}
		calls, exists := graph[name]
		if !exists {
			return 0, fmt.Errorf("notification template %q is not defined", name)
		}
		states[name] = 1
		height := 0
		for _, next := range calls {
			value, err := visit(next, depth+1)
			if err != nil {
				return 0, err
			}
			height = max(height, value+1)
		}
		if height > 32 {
			return 0, errors.New("notification template call depth exceeds 32")
		}
		states[name] = 2
		heights[name] = height
		return height, nil
	}
	for name := range graph {
		if _, err := visit(name, 0); err != nil {
			return err
		}
	}
	return nil
}

type execution struct {
	ctx    context.Context
	steps  int
	output bytes.Buffer
}

func (e *execution) check(cost int) error {
	e.steps += max(1, cost)
	if e.steps > MaxSteps {
		return ErrLimit
	}
	return e.ctx.Err()
}
func (e *execution) Write(p []byte) (int, error) {
	if err := e.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) > MaxOutput-e.output.Len() {
		return 0, ErrLimit
	}
	return e.output.Write(p)
}

// Count values before calling a function, including lists constructed by the
// template itself. This prevents exponential JSON/list expansion before output.
func valueCost(v reflect.Value, depth int, budget *int) error {
	if depth > 16 {
		return ErrLimit
	}
	if !v.IsValid() {
		return nil
	}
	*budget -= 8
	if *budget < 0 {
		return ErrLimit
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if !v.IsNil() {
			return valueCost(v.Elem(), depth+1, budget)
		}
	case reflect.String:
		*budget -= v.Len()
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if err := valueCost(v.Index(i), depth+1, budget); err != nil {
				return err
			}
		}
	case reflect.Map:
		iter := v.MapRange()
		for iter.Next() {
			if err := valueCost(iter.Key(), depth+1, budget); err != nil {
				return err
			}
			if err := valueCost(iter.Value(), depth+1, budget); err != nil {
				return err
			}
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				if err := valueCost(v.Field(i), depth+1, budget); err != nil {
					return err
				}
			}
		}
	}
	if *budget < 0 {
		return ErrLimit
	}
	return nil
}

func (e *execution) wrapped(original any) any {
	fn := reflect.ValueOf(original)
	typ := fn.Type()
	in := make([]reflect.Type, typ.NumIn())
	for i := range in {
		in[i] = typ.In(i)
	}
	out := make([]reflect.Type, typ.NumOut())
	for i := range out {
		out[i] = typ.Out(i)
	}
	errorType := reflect.TypeFor[error]()
	hasError := len(out) > 0 && out[len(out)-1] == errorType
	if !hasError {
		out = append(out, errorType)
	}
	return reflect.MakeFunc(reflect.FuncOf(in, out, typ.IsVariadic()), func(args []reflect.Value) []reflect.Value {
		remaining := MaxOutput
		var err error
		for _, arg := range args {
			if err = valueCost(arg, 0, &remaining); err != nil {
				break
			}
		}
		if err == nil {
			err = e.check(1 + (MaxOutput-remaining)/1024)
		}
		if err != nil {
			values := make([]reflect.Value, len(out))
			for i, t := range out {
				values[i] = reflect.Zero(t)
			}
			values[len(values)-1] = reflect.ValueOf(err)
			return values
		}
		var values []reflect.Value
		if typ.IsVariadic() {
			values = fn.CallSlice(args)
		} else {
			values = fn.Call(args)
		}
		if !hasError {
			values = append(values, reflect.Zero(errorType))
		}
		return values
	}).Interface()
}

func guardedList(list *parse.ListNode, guard parse.Node) {
	if list == nil {
		return
	}
	original := list.Nodes
	list.Nodes = []parse.Node{guard.Copy()}
	for _, node := range original {
		switch n := node.(type) {
		case *parse.RangeNode:
			guardedList(n.List, guard)
			guardedList(n.ElseList, guard)
		case *parse.IfNode:
			guardedList(n.List, guard)
			guardedList(n.ElseList, guard)
		case *parse.WithNode:
			guardedList(n.List, guard)
			guardedList(n.ElseList, guard)
		}
		list.Nodes = append(list.Nodes, guard.Copy(), node)
	}
}

func boundedPrintf(format string, args ...any) (string, error) {
	if len(format) > 4096 {
		return "", ErrLimit
	}
	for _, digits := range numberRuns(format) {
		n, err := strconv.Atoi(digits)
		if err != nil || n > 4096 {
			return "", ErrLimit
		}
	}
	if strings.Contains(format, "*") {
		for _, arg := range args {
			value := reflect.ValueOf(arg)
			if value.IsValid() && value.Kind() >= reflect.Int && value.Kind() <= reflect.Int64 && (value.Int() > 4096 || value.Int() < -4096) {
				return "", ErrLimit
			}
		}
	}
	// fmt builds its complete result before writing. Bound repeated indexed
	// arguments before calling it rather than relying on the writer afterward.
	largest := 0
	for _, arg := range args {
		remaining := MaxOutput
		if err := valueCost(reflect.ValueOf(arg), 0, &remaining); err != nil {
			return "", err
		}
		size := (MaxOutput-remaining)*8 + 4096
		value := reflect.ValueOf(arg)
		if value.IsValid() && value.Kind() == reflect.String {
			size = value.Len() + 4096
			if strings.Contains(format, "q") || strings.Contains(format, "#") {
				size = value.Len()*6 + 4096
			} else if strings.ContainsAny(format, "xX") {
				size = value.Len()*2 + 4096
			}
		}
		largest = max(largest, size)
	}
	conversions := 0
	for i := 0; i < len(format); i++ {
		if format[i] == '%' {
			if i+1 < len(format) && format[i+1] == '%' {
				i++
			} else {
				conversions++
			}
		}
	}
	if largest == 0 {
		largest = 4096
	}
	if conversions > 0 && conversions > (MaxOutput-len(format))/largest {
		return "", ErrLimit
	}
	var output bytes.Buffer
	writer := &limitedWriter{Writer: &output, remaining: MaxOutput}
	_, err := fmt.Fprintf(writer, format, args...)
	return output.String(), err
}

func boundedJoin(separator string, values []string) (string, error) {
	remaining := MaxOutput
	if len(values) > 1 {
		if len(separator) > remaining/(len(values)-1) {
			return "", ErrLimit
		}
		remaining -= len(separator) * (len(values) - 1)
	}
	for _, value := range values {
		if len(value) > remaining {
			return "", ErrLimit
		}
		remaining -= len(value)
	}
	return strings.Join(values, separator), nil
}

func (e *execution) boundedReplace(pattern, replacement, value string) (string, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", err
	}
	if re.NumSubexp() > 128 {
		return "", ErrLimit
	}
	matches := re.FindAllStringSubmatchIndex(value, MaxSteps+1)
	if len(matches) > MaxSteps {
		return "", ErrLimit
	}
	var output bytes.Buffer
	writer := &limitedWriter{Writer: &output, remaining: MaxOutput}
	previous := 0
	for _, match := range matches {
		if err := e.check(1); err != nil {
			return "", err
		}
		if _, err := writer.Write([]byte(value[previous:match[0]])); err != nil {
			return "", err
		}
		capture := 0
		for i := 0; i < len(match); i += 2 {
			if match[i] >= 0 {
				capture = max(capture, match[i+1]-match[i])
			}
		}
		// Each dollar expansion can copy at most the largest capture. Check
		// before regexp.ExpandString allocates a result for repeated $0/$name.
		dollars := strings.Count(replacement, "$")
		if len(replacement) > writer.remaining || (capture > 0 && dollars > (writer.remaining-len(replacement))/capture) {
			return "", ErrLimit
		}
		if _, err := writer.Write(re.ExpandString(nil, replacement, value, match)); err != nil {
			return "", err
		}
		previous = match[1]
	}
	if _, err := writer.Write([]byte(value[previous:])); err != nil {
		return "", err
	}
	return output.String(), nil
}
func numberRuns(s string) []string {
	out := []string{}
	for i := 0; i < len(s); i++ {
		if s[i] != '%' {
			continue
		}
		i++
		for i < len(s) {
			start := i
			for i < len(s) && s[i] >= '0' && s[i] <= '9' {
				i++
			}
			if i > start {
				out = append(out, s[start:i])
			}
			if i >= len(s) || strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ%", rune(s[i])) {
				break
			}
			i++
		}
	}
	return out
}

type limitedWriter struct {
	io.Writer
	remaining int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if len(p) > w.remaining {
		return 0, ErrLimit
	}
	w.remaining -= len(p)
	return w.Writer.Write(p)
}

func (c *Compiled) Render(ctx context.Context, expression string, data Data) (string, error) {
	if len(expression) > 65536 || strings.Contains(expression, guardName) {
		return "", errors.New("invalid notification template expression")
	}
	clone, err := c.base.Clone()
	if err != nil {
		return "", err
	}
	e := &execution{ctx: ctx}
	funcs := texttemplate.FuncMap{}
	for name, fn := range amtemplate.DefaultFuncs {
		funcs[name] = e.wrapped(fn)
	}
	funcs["printf"] = e.wrapped(boundedPrintf)
	funcs["join"] = e.wrapped(boundedJoin)
	funcs["reReplaceAll"] = e.wrapped(e.boundedReplace)
	funcs["html"] = e.wrapped(texttemplate.HTMLEscaper)
	funcs["js"] = e.wrapped(texttemplate.JSEscaper)
	funcs["urlquery"] = e.wrapped(texttemplate.URLQueryEscaper)
	funcs["print"] = e.wrapped(func(args ...any) (string, error) {
		var buffer bytes.Buffer
		_, err := fmt.Fprint(&limitedWriter{Writer: &buffer, remaining: MaxOutput}, args...)
		return buffer.String(), err
	})
	funcs["println"] = e.wrapped(func(args ...any) (string, error) {
		var buffer bytes.Buffer
		_, err := fmt.Fprintln(&limitedWriter{Writer: &buffer, remaining: MaxOutput}, args...)
		return buffer.String(), err
	})
	funcs[guardName] = func() (string, error) { return "", e.check(1) }
	funcs["routeLabels"] = e.wrapped(func(name string) string { return data.RouteLabels[name] })
	clone.Funcs(funcs)
	run, err := clone.New("__metricspanel_render").Option("missingkey=zero").Parse(expression)
	if err != nil {
		return "", err
	}
	if err = validateTrees(clone); err != nil {
		return "", err
	}
	parsedGuard, err := texttemplate.New("guard").Funcs(funcs).Parse("{{" + guardName + "}}")
	if err != nil {
		return "", err
	}
	guard := parsedGuard.Tree.Root.Nodes[0]
	for _, t := range clone.Templates() {
		if t.Tree != nil {
			t.Tree = t.Tree.Copy()
			guardedList(t.Tree.Root, guard)
		}
	}
	if err = run.Execute(e, data); err != nil {
		return "", err
	}
	return e.output.String(), nil
}

func (c *Compiled) ValidateExpression(expression string) error {
	clone, err := c.base.Clone()
	if err != nil {
		return err
	}
	if strings.Contains(expression, guardName) || len(expression) > 65536 {
		return errors.New("invalid notification template expression")
	}
	if _, err := clone.New("__metricspanel_render").Parse(expression); err != nil {
		return err
	}
	return validateTrees(clone)
}
