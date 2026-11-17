package netbox

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Rule expressions: facts compared with values, combined with && || ! and
// parentheses.
//
//	memory_gb >= 192 && cores >= 16
//	verdict == "FAIL" || failed_parts > 0
//	model ~ "DL3[68]0" && !virtual
//
// == and != compare text without regard to case; ~ and !~ match a regular
// expression (case-insensitive). A fact alone is true when it is true, a
// number other than 0 or text that is not empty.

// Expr is a parsed rule.
type Expr struct {
	src  string
	root node
}

func (e *Expr) String() string { return e.src }

type kind int

const (
	kNum kind = iota
	kStr
	kBool
)

func (k kind) String() string { return [...]string{"number", "text", "true/false"}[k] }

// Value is a fact's value.
type Value struct {
	K kind
	N float64
	S string
	B bool
}

// Num, Str and Bool make values.
func Num(n float64) Value { return Value{K: kNum, N: n} }
func Str(s string) Value  { return Value{K: kStr, S: s} }
func Bool(b bool) Value   { return Value{K: kBool, B: b} }

func (v Value) String() string {
	switch v.K {
	case kNum:
		return strconv.FormatFloat(v.N, 'f', -1, 64)
	case kBool:
		return strconv.FormatBool(v.B)
	}
	return strconv.Quote(v.S)
}

func (v Value) truthy() bool {
	switch v.K {
	case kNum:
		return v.N != 0
	case kBool:
		return v.B
	}
	return v.S != ""
}

type node interface {
	eval(f Facts) (Value, error)
}

type (
	litNode   struct{ v Value }
	factNode  struct{ name string }
	notNode   struct{ x node }
	logicNode struct {
		and  bool
		l, r node
	}
	cmpNode struct {
		op   string
		l, r node
		re   *regexp.Regexp // ~ and !~ with a literal pattern
	}
)

func (n litNode) eval(Facts) (Value, error) { return n.v, nil }

func (n factNode) eval(f Facts) (Value, error) {
	v, ok := f[n.name]
	if !ok {
		return Value{}, fmt.Errorf("unknown fact %q", n.name)
	}
	return v, nil
}

func (n notNode) eval(f Facts) (Value, error) {
	v, err := n.x.eval(f)
	return Bool(!v.truthy()), err
}

func (n logicNode) eval(f Facts) (Value, error) {
	l, err := n.l.eval(f)
	if err != nil {
		return Value{}, err
	}
	if n.and && !l.truthy() {
		return Bool(false), nil
	}
	if !n.and && l.truthy() {
		return Bool(true), nil
	}
	r, err := n.r.eval(f)
	return Bool(r.truthy()), err
}

func (n cmpNode) eval(f Facts) (Value, error) {
	l, err := n.l.eval(f)
	if err != nil {
		return Value{}, err
	}
	r, err := n.r.eval(f)
	if err != nil {
		return Value{}, err
	}
	switch n.op {
	case "~", "!~":
		re := n.re
		if re == nil {
			if re, err = regexp.Compile("(?i)" + r.S); err != nil {
				return Value{}, err
			}
		}
		m := re.MatchString(plain(l))
		return Bool(m == (n.op == "~")), nil
	}
	if l.K != r.K {
		return Value{}, fmt.Errorf("%s %s %s: cannot compare %s with %s", l, n.op, r, l.K, r.K)
	}
	var c int
	switch l.K {
	case kNum:
		c = cmpFloat(l.N, r.N)
	case kStr:
		c = strings.Compare(strings.ToLower(l.S), strings.ToLower(r.S))
	case kBool:
		if n.op != "==" && n.op != "!=" {
			return Value{}, fmt.Errorf("%s on true/false", n.op)
		}
		if l.B != r.B {
			c = 1
		}
	}
	switch n.op {
	case "==":
		return Bool(c == 0), nil
	case "!=":
		return Bool(c != 0), nil
	case "<":
		return Bool(c < 0), nil
	case "<=":
		return Bool(c <= 0), nil
	case ">":
		return Bool(c > 0), nil
	}
	return Bool(c >= 0), nil
}

func plain(v Value) string {
	if v.K == kStr {
		return v.S
	}
	return v.String()
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// Eval evaluates the rule against the facts.
func (e *Expr) Eval(f Facts) (bool, error) {
	v, err := e.root.eval(f)
	if err != nil {
		return false, err
	}
	return v.truthy(), nil
}

// --- parser --------------------------------------------------------------

type token struct {
	typ string // num, str, ident, op, (, ), eof
	s   string
	pos int
}

func lex(src string) ([]token, error) {
	var out []token
	rs := []rune(src)
	for i := 0; i < len(rs); {
		c := rs[i]
		switch {
		case unicode.IsSpace(c):
			i++
		case c == '(' || c == ')':
			out = append(out, token{string(c), string(c), i})
			i++
		case c == '"' || c == '\'':
			j := i + 1
			var b strings.Builder
			for j < len(rs) && rs[j] != c {
				if rs[j] == '\\' && j+1 < len(rs) && rs[j+1] == c {
					j++ // \" is a quote; other backslashes stay (regular expressions)
				}
				b.WriteRune(rs[j])
				j++
			}
			if j >= len(rs) {
				return nil, fmt.Errorf("position %d: text not closed with %c", i+1, c)
			}
			out = append(out, token{"str", b.String(), i})
			i = j + 1
		case unicode.IsDigit(c) || (c == '-' && i+1 < len(rs) && unicode.IsDigit(rs[i+1])):
			j := i + 1
			for j < len(rs) && (unicode.IsDigit(rs[j]) || rs[j] == '.') {
				j++
			}
			out = append(out, token{"num", string(rs[i:j]), i})
			i = j
		case unicode.IsLetter(c) || c == '_':
			j := i + 1
			for j < len(rs) && (unicode.IsLetter(rs[j]) || unicode.IsDigit(rs[j]) || rs[j] == '_') {
				j++
			}
			out = append(out, token{"ident", string(rs[i:j]), i})
			i = j
		default:
			two := ""
			if i+1 < len(rs) {
				two = string(rs[i : i+2])
			}
			switch two {
			case "&&", "||", "==", "!=", "<=", ">=", "!~":
				out = append(out, token{"op", two, i})
				i += 2
				continue
			}
			switch c {
			case '<', '>', '!', '~':
				out = append(out, token{"op", string(c), i})
				i++
			case '=':
				return nil, fmt.Errorf("position %d: use == to compare", i+1)
			default:
				return nil, fmt.Errorf("position %d: unexpected %q", i+1, c)
			}
		}
	}
	return append(out, token{"eof", "", len(rs)}), nil
}

type parser struct {
	toks []token
	i    int
}

func (p *parser) peek() token { return p.toks[p.i] }
func (p *parser) next() token { t := p.toks[p.i]; p.i++; return t }

// Parse parses a rule; facts it names must exist (FactNames).
func Parse(src string) (*Expr, error) {
	if strings.TrimSpace(src) == "" {
		return nil, fmt.Errorf("empty condition")
	}
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	n, err := p.or()
	if err != nil {
		return nil, err
	}
	if t := p.peek(); t.typ != "eof" {
		return nil, fmt.Errorf("position %d: unexpected %q", t.pos+1, t.s)
	}
	return &Expr{src: src, root: n}, nil
}

func (p *parser) or() (node, error) {
	l, err := p.and()
	for err == nil && p.peek().s == "||" {
		p.next()
		var r node
		if r, err = p.and(); err == nil {
			l = logicNode{and: false, l: l, r: r}
		}
	}
	return l, err
}

func (p *parser) and() (node, error) {
	l, err := p.unary()
	for err == nil && p.peek().s == "&&" {
		p.next()
		var r node
		if r, err = p.unary(); err == nil {
			l = logicNode{and: true, l: l, r: r}
		}
	}
	return l, err
}

func (p *parser) unary() (node, error) {
	if t := p.peek(); t.typ == "op" && t.s == "!" {
		p.next()
		x, err := p.unary()
		return notNode{x}, err
	}
	return p.cmp()
}

func (p *parser) cmp() (node, error) {
	l, err := p.primary()
	if err != nil {
		return nil, err
	}
	t := p.peek()
	if t.typ != "op" || t.s == "&&" || t.s == "||" || t.s == "!" {
		return l, nil
	}
	p.next()
	r, err := p.primary()
	if err != nil {
		return nil, err
	}
	c := cmpNode{op: t.s, l: l, r: r}
	if t.s == "~" || t.s == "!~" {
		lit, ok := r.(litNode)
		if !ok || lit.v.K != kStr {
			return nil, fmt.Errorf("position %d: %s needs a pattern in quotes", t.pos+1, t.s)
		}
		if c.re, err = regexp.Compile("(?i)" + lit.v.S); err != nil {
			return nil, fmt.Errorf("pattern %q: %w", lit.v.S, err)
		}
	}
	// compare types early where both sides are known
	if k1, ok1 := staticKind(l); ok1 {
		if k2, ok2 := staticKind(r); ok2 && k1 != k2 && t.s != "~" && t.s != "!~" {
			return nil, fmt.Errorf("position %d: cannot compare %s with %s", t.pos+1, k1, k2)
		}
	}
	return c, nil
}

func staticKind(n node) (kind, bool) {
	switch x := n.(type) {
	case litNode:
		return x.v.K, true
	case factNode:
		if d, ok := factIndex[x.name]; ok {
			return d.Kind, true
		}
	}
	return 0, false
}

func (p *parser) primary() (node, error) {
	t := p.next()
	switch t.typ {
	case "num":
		n, err := strconv.ParseFloat(t.s, 64)
		if err != nil {
			return nil, fmt.Errorf("position %d: number %q", t.pos+1, t.s)
		}
		return litNode{Num(n)}, nil
	case "str":
		return litNode{Str(t.s)}, nil
	case "ident":
		switch t.s {
		case "true":
			return litNode{Bool(true)}, nil
		case "false":
			return litNode{Bool(false)}, nil
		}
		if _, ok := factIndex[t.s]; !ok {
			return nil, fmt.Errorf("position %d: unknown fact %q (see the list of facts)", t.pos+1, t.s)
		}
		return factNode{t.s}, nil
	case "(":
		n, err := p.or()
		if err != nil {
			return nil, err
		}
		if c := p.next(); c.typ != ")" {
			return nil, fmt.Errorf("position %d: missing )", c.pos+1)
		}
		return n, nil
	case "eof":
		return nil, fmt.Errorf("incomplete condition")
	}
	return nil, fmt.Errorf("position %d: unexpected %q", t.pos+1, t.s)
}
