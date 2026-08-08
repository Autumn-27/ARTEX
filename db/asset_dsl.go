package db

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// Expr is one leaf DSL clause.
type Expr struct {
	Field string // empty = bare-text full-text search
	Op    string // "=", "==", "!=", ">", ">=", "<", "<="
	Value string
}

// astNode is a node in the parsed DSL expression tree.
type astNode struct {
	kind     string // "and", "or", "leaf"
	children []*astNode
	expr     *Expr // only for "leaf"
}

func andNode(cs []*astNode) *astNode { return &astNode{kind: "and", children: cs} }
func orNode(cs []*astNode) *astNode  { return &astNode{kind: "or", children: cs} }
func leafNode(e Expr) *astNode       { return &astNode{kind: "leaf", expr: &e} }

// knownStringFields maps DSL field name → SQL column name.
// NOTE: "type" is intentionally excluded — it is a separate parameter, not a DSL field.
var knownStringFields = map[string]string{
	"domain":       "domain",
	"root_domain":  "root_domain",
	"ip":           "ip",
	"url":          "url",
	"page_title":   "page_title",
	"title":        "page_title",
	"icp":          "icp",
	"service_name": "service_name",
	"app_name":     "app_name",
	"bundle_id":    "bundle_id",
	"category":     "category",
	"app_icp":      "app_icp",
	"method":       "method",
	"service_type": "service_type",
	"record_type":  "record_type",
}

// knownArrayFields maps DSL field name → SQL column name (array).
var knownArrayFields = map[string]string{
	"technology":   "technologies",
	"technologies": "technologies",
	"tech":         "technologies",
}

// knownNumericFields maps DSL field name → SQL column name (integer).
var knownNumericFields = map[string]string{
	"port":        "port",
	"status_code": "status_code",
	"status":      "status_code",
}

func isKnownField(f string) bool {
	f = strings.ToLower(f)
	_, s := knownStringFields[f]
	_, a := knownArrayFields[f]
	_, n := knownNumericFields[f]
	return s || a || n || f == "company_id" || f == "task_id"
}

// ── tokeniser ────────────────────────────────────────────────────────────────

const (
	tkField = "FIELD"
	tkBare  = "BARE"
	tkAnd   = "AND"
	tkOr    = "OR"
	tkLP    = "LPAREN"
	tkRP    = "RPAREN"
	tkEOF   = "EOF"
)

type tok struct {
	kind string
	expr *Expr // set for tkField and tkBare
}

func tokenize(s string) ([]tok, error) {
	var tokens []tok
	i := 0
	for i < len(s) {
		for i < len(s) && unicode.IsSpace(rune(s[i])) {
			i++
		}
		if i >= len(s) {
			break
		}
		switch s[i] {
		case '(':
			tokens = append(tokens, tok{kind: tkLP})
			i++
		case ')':
			tokens = append(tokens, tok{kind: tkRP})
			i++
		default:
			if expr, end, ok := tryParseFieldExpr(s, i); ok {
				tokens = append(tokens, tok{kind: tkField, expr: &expr})
				i = end
				continue
			}
			word, end := readToken(s, i)
			if word == "" {
				i++
				continue
			}
			switch strings.ToUpper(word) {
			case "AND":
				tokens = append(tokens, tok{kind: tkAnd})
			case "OR":
				tokens = append(tokens, tok{kind: tkOr})
			default:
				e := Expr{Field: "", Op: "=", Value: word}
				tokens = append(tokens, tok{kind: tkBare, expr: &e})
			}
			i = end
		}
	}
	tokens = append(tokens, tok{kind: tkEOF})
	return tokens, nil
}

// ── parser ───────────────────────────────────────────────────────────────────
//
// Grammar (AND binds tighter than OR):
//   expr     = or_expr
//   or_expr  = and_expr (OR and_expr)*
//   and_expr = atom    (AND atom)*
//   atom     = FIELD | BARE | '(' expr ')'

type dslParser struct {
	tokens []tok
	pos    int
}

func (p *dslParser) peek() tok {
	if p.pos >= len(p.tokens) {
		return tok{kind: tkEOF}
	}
	return p.tokens[p.pos]
}

func (p *dslParser) consume() tok {
	t := p.peek()
	p.pos++
	return t
}

func (p *dslParser) parseOr() (*astNode, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	children := []*astNode{left}
	for p.peek().kind == tkOr {
		p.consume()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		children = append(children, right)
	}
	if len(children) == 1 {
		return children[0], nil
	}
	return orNode(children), nil
}

func (p *dslParser) parseAnd() (*astNode, error) {
	left, err := p.parseAtom()
	if err != nil {
		return nil, err
	}
	children := []*astNode{left}
	for p.peek().kind == tkAnd {
		p.consume()
		right, err := p.parseAtom()
		if err != nil {
			return nil, err
		}
		children = append(children, right)
	}
	if len(children) == 1 {
		return children[0], nil
	}
	return andNode(children), nil
}

func (p *dslParser) parseAtom() (*astNode, error) {
	t := p.peek()
	switch t.kind {
	case tkField, tkBare:
		p.consume()
		return leafNode(*t.expr), nil
	case tkLP:
		p.consume()
		node, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if p.peek().kind != tkRP {
			return nil, fmt.Errorf("DSL 语法错误：缺少右括号 ')'")
		}
		p.consume()
		return node, nil
	case tkEOF:
		return nil, fmt.Errorf("DSL 语法错误：表达式不完整")
	default:
		return nil, fmt.Errorf("DSL 语法错误：意外的 token '%s'", t.kind)
	}
}

// ParseDSL parses a DSL query string into an expression tree.
//
// Syntax:
//
//	field=value      fuzzy match (ILIKE '%value%')
//	field==value     exact match
//	field!=value     exclude fuzzy
//	port>8080        numeric comparison
//	bare word        full-text fuzzy across all main text fields
//
// Operators: AND OR (case-insensitive), parentheses for grouping.
// AND binds tighter than OR.
func ParseDSL(s string) (*astNode, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	tokens, err := tokenize(s)
	if err != nil {
		return nil, err
	}
	p := &dslParser{tokens: tokens}
	node, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != tkEOF {
		return nil, fmt.Errorf("DSL 语法错误：意外的内容 '%s'", p.peek().kind)
	}
	return node, nil
}

// ── SQL builder ──────────────────────────────────────────────────────────────

// fullTextCols are searched for bare-text tokens.
var fullTextCols = []string{
	"domain", "root_domain", "ip", "url", "page_title",
	"icp", "service_name", "app_name", "app_description",
}

type whereBuilder struct {
	args []any
}

func (b *whereBuilder) next(v any) string {
	b.args = append(b.args, v)
	return fmt.Sprintf("$%d", len(b.args))
}

func (b *whereBuilder) build(node *astNode) (string, error) {
	switch node.kind {
	case "and":
		parts := make([]string, 0, len(node.children))
		for _, child := range node.children {
			clause, err := b.build(child)
			if err != nil {
				return "", err
			}
			parts = append(parts, "("+clause+")")
		}
		return strings.Join(parts, " AND "), nil
	case "or":
		parts := make([]string, 0, len(node.children))
		for _, child := range node.children {
			clause, err := b.build(child)
			if err != nil {
				return "", err
			}
			parts = append(parts, "("+clause+")")
		}
		return strings.Join(parts, " OR "), nil
	case "leaf":
		return b.buildLeaf(*node.expr)
	}
	return "", fmt.Errorf("unknown node kind: %s", node.kind)
}

func (b *whereBuilder) buildLeaf(e Expr) (string, error) {
	f := strings.ToLower(e.Field)

	// bare-text: OR across all text fields + arrays
	if f == "" {
		p := b.next("%" + e.Value + "%")
		var parts []string
		for _, col := range fullTextCols {
			parts = append(parts, col+" ILIKE "+p)
		}
		parts = append(parts,
			"EXISTS (SELECT 1 FROM unnest(technologies) t(v) WHERE v ILIKE "+p+")",
			"EXISTS (SELECT 1 FROM unnest(bound_domains) t(v) WHERE v ILIKE "+p+")",
		)
		return "(" + strings.Join(parts, " OR ") + ")", nil
	}

	// task_id: $N = ANY(task_ids)
	if f == "task_id" {
		n, err := strconv.ParseInt(e.Value, 10, 64)
		if err != nil {
			return "", fmt.Errorf("task_id 需要整数值: %s", e.Value)
		}
		return b.next(n) + " = ANY(task_ids)", nil
	}

	// company_id: exact integer
	if f == "company_id" {
		n, err := strconv.ParseInt(e.Value, 10, 64)
		if err != nil {
			return "", fmt.Errorf("company_id 需要整数值: %s", e.Value)
		}
		return "company_id = " + b.next(n), nil
	}

	// numeric fields
	if col, ok := knownNumericFields[f]; ok {
		n, err := strconv.Atoi(e.Value)
		if err != nil {
			return "", fmt.Errorf("字段 %s 需要整数值: %s", f, e.Value)
		}
		op := e.Op
		if op == "==" {
			op = "="
		}
		if op != "=" && op != "!=" && op != ">" && op != ">=" && op != "<" && op != "<=" {
			return "", fmt.Errorf("字段 %s 不支持运算符 %s", f, e.Op)
		}
		return fmt.Sprintf("%s %s %s", col, op, b.next(n)), nil
	}

	// array fields
	if col, ok := knownArrayFields[f]; ok {
		switch e.Op {
		case "==":
			return b.next(e.Value) + " = ANY(" + col + ")", nil
		case "!=":
			return "NOT (" + b.next(e.Value) + " = ANY(" + col + "))", nil
		case "=":
			p := b.next("%" + e.Value + "%")
			return "EXISTS (SELECT 1 FROM unnest(" + col + ") t(v) WHERE v ILIKE " + p + ")", nil
		default:
			return "", fmt.Errorf("数组字段 %s 不支持运算符 %s", f, e.Op)
		}
	}

	// string fields
	if col, ok := knownStringFields[f]; ok {
		switch e.Op {
		case "=":
			return col + " ILIKE " + b.next("%"+e.Value+"%"), nil
		case "==":
			return col + " = " + b.next(e.Value), nil
		case "!=":
			return col + " NOT ILIKE " + b.next("%"+e.Value+"%"), nil
		default:
			return "", fmt.Errorf("字符串字段 %s 不支持运算符 %s", f, e.Op)
		}
	}

	return "", fmt.Errorf("未知字段: %s", f)
}

func buildDSLWhere(node *astNode) (string, []any, error) {
	if node == nil {
		return "1=1", nil, nil
	}
	b := &whereBuilder{}
	clause, err := b.build(node)
	if err != nil {
		return "", nil, err
	}
	return clause, b.args, nil
}

// ── helpers (shared with parser) ─────────────────────────────────────────────

// tryParseFieldExpr tries to parse "field op value" at pos.
func tryParseFieldExpr(s string, pos int) (Expr, int, bool) {
	i := pos
	if i >= len(s) || !isIdentStart(s[i]) {
		return Expr{}, pos, false
	}
	for i < len(s) && isIdentChar(s[i]) {
		i++
	}
	field := strings.ToLower(s[pos:i])
	if !isKnownField(field) {
		return Expr{}, pos, false
	}
	if i >= len(s) {
		return Expr{}, pos, false
	}
	var op string
	switch {
	case i+1 < len(s) && (s[i] == '=' || s[i] == '!' || s[i] == '>' || s[i] == '<') && s[i+1] == '=':
		op = s[i : i+2]
		i += 2
	case s[i] == '>' || s[i] == '<' || s[i] == '=':
		op = string(s[i])
		i++
	default:
		return Expr{}, pos, false
	}
	value, end := readToken(s, i)
	if end == i {
		return Expr{}, pos, false
	}
	return Expr{Field: field, Op: op, Value: value}, end, true
}

// readToken reads a quoted or unquoted token starting at pos.
func readToken(s string, pos int) (string, int) {
	if pos >= len(s) {
		return "", pos
	}
	if s[pos] == '"' {
		i := pos + 1
		for i < len(s) && s[i] != '"' {
			i++
		}
		val := s[pos+1 : i]
		if i < len(s) {
			i++
		}
		return val, i
	}
	i := pos
	for i < len(s) && !unicode.IsSpace(rune(s[i])) && s[i] != '(' && s[i] != ')' {
		i++
	}
	return s[pos:i], i
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
func isIdentChar(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

// ── QueryDSL ─────────────────────────────────────────────────────────────────

// CountDSL returns the total number of assets matching a DSL expression (and optional
// type), for server-side pagination — same WHERE as QueryDSL, without LIMIT/OFFSET.
func (s *AssetStore) CountDSL(dsl, typ string) (int, error) {
	node, err := ParseDSL(dsl)
	if err != nil {
		return 0, err
	}
	where, args, err := buildDSLWhere(node)
	if err != nil {
		return 0, err
	}
	if typ != "" {
		args = append(args, typ)
		where += fmt.Sprintf(" AND type = $%d", len(args))
	}
	var n int
	err = s.db.QueryRow("SELECT count(*) FROM assets WHERE "+where, args...).Scan(&n)
	return n, err
}

// QueryDSL executes a DSL query string against the asset store.
// typ is an optional asset type filter applied independently of the DSL expression.
func (s *AssetStore) QueryDSL(dsl, typ string, limit, offset int) ([]*Asset, error) {
	if limit <= 0 {
		limit = 50
	}
	node, err := ParseDSL(dsl)
	if err != nil {
		return nil, err
	}
	where, args, err := buildDSLWhere(node)
	if err != nil {
		return nil, err
	}
	if typ != "" {
		args = append(args, typ)
		where += fmt.Sprintf(" AND type = $%d", len(args))
	}
	args = append(args, limit, offset)
	q := assetSelectCols + " WHERE " + where +
		fmt.Sprintf(" ORDER BY last_seen DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args))
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAssets(rows)
}
