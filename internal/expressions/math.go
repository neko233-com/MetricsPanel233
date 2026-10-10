package expressions

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
)

type token struct{ text, kind string }
type mathNode struct {
	kind, text string
	number     *float64
	args       []*mathNode
}
type mathParser struct {
	tokens     []token
	pos, nodes int
	refs       map[string]bool
}

func parseMath(expression string) (*mathNode, []string, error) {
	if expression == "" || len(expression) > 10000 {
		return nil, nil, errors.New("math needs an expression of 1–10000 bytes")
	}
	tokens, err := lexMath(expression)
	if err != nil {
		return nil, nil, err
	}
	p := mathParser{tokens: tokens, refs: map[string]bool{}}
	root, err := p.parse(0, 0)
	if err != nil {
		return nil, nil, err
	}
	if p.pos != len(p.tokens) {
		return nil, nil, fmt.Errorf("unexpected math token %q", p.tokens[p.pos].text)
	}
	return root, sortedRefs(p.refs), nil
}
func lexMath(input string) ([]token, error) {
	out := []token{}
	for i := 0; i < len(input); {
		c := input[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			i++
			continue
		}
		start := i
		if c == '$' {
			i++
			if i < len(input) && input[i] == '{' {
				i++
				start = i
				for i < len(input) && input[i] != '}' {
					i++
				}
				if i == len(input) || i == start {
					return nil, errors.New("invalid braced expression reference")
				}
				out = append(out, token{text: input[start:i], kind: "ref"})
				i++
				continue
			}
			start = i
			for i < len(input) && (input[i] == '_' || unicode.IsLetter(rune(input[i])) || input[i] >= '0' && input[i] <= '9') {
				i++
			}
			if i == start {
				return nil, errors.New("empty expression reference")
			}
			out = append(out, token{text: input[start:i], kind: "ref"})
			continue
		}
		if c >= '0' && c <= '9' || c == '.' {
			i++
			for i < len(input) {
				ch := input[i]
				if ch >= '0' && ch <= '9' || ch == '.' || ch >= 'a' && ch <= 'f' || ch >= 'A' && ch <= 'F' || ch == 'x' || ch == 'X' {
					i++
					continue
				}
				if (ch == '+' || ch == '-') && (input[i-1] == 'e' || input[i-1] == 'E') && !strings.HasPrefix(strings.ToLower(input[start:i]), "0x") {
					i++
					continue
				}
				break
			}
			out = append(out, token{text: input[start:i], kind: "number"})
			continue
		}
		if c >= 'a' && c <= 'z' || c == '_' {
			i++
			for i < len(input) && (input[i] >= 'a' && input[i] <= 'z' || input[i] == '_') {
				i++
			}
			out = append(out, token{text: input[start:i], kind: "function"})
			continue
		}
		if i+1 < len(input) && strings.Contains(" ** && || == != <= >= ", " "+input[i:i+2]+" ") {
			out = append(out, token{text: input[i : i+2], kind: "op"})
			i += 2
			continue
		}
		if strings.ContainsRune("+-*/%><!(),", rune(c)) {
			out = append(out, token{text: string(c), kind: "op"})
			i++
			continue
		}
		return nil, fmt.Errorf("invalid math character at byte %d", i)
	}
	if len(out) > 2048 {
		return nil, errors.New("math token limit exceeded")
	}
	return out, nil
}
func precedence(op string) int {
	switch op {
	case "||":
		return 1
	case "&&":
		return 2
	case "==", "!=", ">", "<", ">=", "<=":
		return 3
	case "+", "-":
		return 4
	case "*", "/", "%":
		return 5
	case "**":
		return 7
	}
	return -1
}
func (p *mathParser) consume(t string) bool {
	if p.pos < len(p.tokens) && p.tokens[p.pos].text == t {
		p.pos++
		return true
	}
	return false
}
func (p *mathParser) parse(min, depth int) (*mathNode, error) {
	p.nodes++
	if p.nodes > 1024 || depth > 64 {
		return nil, errors.New("math syntax complexity limit exceeded")
	}
	if p.pos == len(p.tokens) {
		return nil, errors.New("incomplete math expression")
	}
	t := p.tokens[p.pos]
	p.pos++
	var root *mathNode
	switch {
	case t.kind == "number":
		var n float64
		var err error
		if strings.HasPrefix(t.text, "0x") || strings.HasPrefix(t.text, "0X") || len(t.text) > 1 && t.text[0] == '0' && !strings.ContainsAny(t.text, ".eE") {
			var integer int64
			integer, err = strconv.ParseInt(t.text, 0, 64)
			n = float64(integer)
		} else {
			n, err = strconv.ParseFloat(t.text, 64)
		}
		if err != nil || math.IsInf(n, 0) {
			return nil, fmt.Errorf("invalid math number %q", t.text)
		}
		root = &mathNode{kind: "number", number: ptr(n)}
	case t.kind == "ref":
		p.refs[t.text] = true
		root = &mathNode{kind: "ref", text: t.text}
	case t.text == "(":
		var err error
		root, err = p.parse(0, depth+1)
		if err != nil {
			return nil, err
		}
		if !p.consume(")") {
			return nil, errors.New("missing math closing parenthesis")
		}
	case t.text == "-" || t.text == "!" || t.text == "+":
		argument, err := p.parse(8, depth+1)
		if err != nil {
			return nil, err
		}
		root = &mathNode{kind: "unary", text: t.text, args: []*mathNode{argument}}
	case t.kind == "function":
		arity, ok := functionArity[t.text]
		if !ok {
			return nil, fmt.Errorf("unknown math function %q", t.text)
		}
		if !p.consume("(") {
			return nil, errors.New("math functions need parentheses")
		}
		root = &mathNode{kind: "function", text: t.text}
		if !p.consume(")") {
			for {
				argument, err := p.parse(0, depth+1)
				if err != nil {
					return nil, err
				}
				root.args = append(root.args, argument)
				if p.consume(")") {
					break
				}
				if !p.consume(",") {
					return nil, errors.New("missing math function delimiter")
				}
			}
		}
		if len(root.args) != arity {
			return nil, fmt.Errorf("math function %s needs %d arguments", t.text, arity)
		}
	default:
		return nil, fmt.Errorf("unexpected math token %q", t.text)
	}
	for p.pos < len(p.tokens) {
		op := p.tokens[p.pos].text
		level := precedence(op)
		if level < min {
			break
		}
		p.pos++
		next := level + 1
		// Grafana's exponent operator is left associative; unary operators
		// bind more tightly than exponentiation in its public parser.
		right, err := p.parse(next, depth+1)
		if err != nil {
			return nil, err
		}
		root = &mathNode{kind: "binary", text: op, args: []*mathNode{root, right}}
	}
	return root, nil
}

var functionArity = map[string]int{"abs": 1, "log": 1, "round": 1, "ceil": 1, "floor": 1, "is_nan": 1, "is_inf": 1, "is_null": 1, "is_number": 1, "nan": 0, "inf": 0, "infn": 0, "null": 0}

func (n *mathNode) evaluate(vars map[string]values, b *budget) (values, error) {
	if err := b.step(); err != nil {
		return nil, err
	}
	if n.kind == "ref" {
		v, ok := vars[n.text]
		if !ok {
			return nil, fmt.Errorf("missing expression input %q", n.text)
		}
		return v, nil
	}
	if n.kind == "number" {
		if err := b.allocate(1); err != nil {
			return nil, err
		}
		return values{{points: []*float64{n.number}, scalar: true}}, nil
	}
	arguments := make([]values, len(n.args))
	for i, arg := range n.args {
		v, err := arg.evaluate(vars, b)
		if err != nil {
			return nil, err
		}
		arguments[i] = v
	}
	if n.kind == "binary" {
		return binaryValues(arguments[0], arguments[1], n.text, b)
	}
	if len(arguments) == 0 {
		var v *float64
		switch n.text {
		case "nan":
			v = ptr(math.NaN())
		case "inf":
			v = ptr(math.Inf(1))
		case "infn":
			v = ptr(math.Inf(-1))
		}
		if err := b.allocate(1); err != nil {
			return nil, err
		}
		return values{{points: []*float64{v}, scalar: true}}, nil
	}
	result := values{}
	for _, v := range arguments[0] {
		mapped, err := clonePoints(v, b, func(f *float64) *float64 {
			if n.kind == "unary" {
				if f == nil {
					return nil
				}
				if math.IsNaN(*f) {
					return ptr(math.NaN())
				}
				switch n.text {
				case "-":
					return ptr(-*f)
				case "!":
					return boolean(*f == 0)
				default:
					return ptr(*f)
				}
			}
			switch n.text {
			case "is_null":
				return boolean(f == nil)
			case "is_number":
				return boolean(!nonNumber(f))
			default:
				if f == nil {
					return ptr(math.NaN())
				}
				switch n.text {
				case "is_nan":
					return boolean(math.IsNaN(*f))
				case "is_inf":
					return boolean(math.IsInf(*f, 0))
				case "abs":
					return ptr(math.Abs(*f))
				case "log":
					return ptr(math.Log(*f))
				case "round":
					return ptr(math.Round(*f))
				case "ceil":
					return ptr(math.Ceil(*f))
				case "floor":
					return ptr(math.Floor(*f))
				}
			}
			panic("unvalidated math function")
		})
		if err != nil {
			return nil, err
		}
		result = append(result, mapped)
	}
	return result, nil
}
