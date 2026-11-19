// Package calculator 计算仅含 + - * / 与括号的整数表达式。
package calculator

import (
	"errors"
	"strconv"
	"strings"
	"unicode"
)

// ErrInvalidExpression 表示表达式无法解析。
var ErrInvalidExpression = errors.New("invalid expression")

// ErrDivideByZero 表示除数为零。
var ErrDivideByZero = errors.New("divide by zero")

// Eval 计算整数表达式。
func Eval(expr string) (int, error) {
	p := &parser{input: strings.ReplaceAll(expr, " ", "")}
	value, err := p.parseSum()
	if err != nil {
		return 0, err
	}
	if p.pos != len(p.input) {
		return 0, ErrInvalidExpression
	}
	return value, nil
}

type parser struct {
	input string
	pos   int
}

func (p *parser) parseSum() (int, error) {
	value, err := p.parseProduct()
	if err != nil {
		return 0, err
	}
	for p.pos < len(p.input) {
		op := p.input[p.pos]
		if op != '+' && op != '-' {
			return value, nil
		}
		p.pos++
		right, err := p.parseProduct()
		if err != nil {
			return 0, err
		}
		if op == '+' {
			value += right
		} else {
			value -= right
		}
	}
	return value, nil
}

func (p *parser) parseProduct() (int, error) {
	value, err := p.parsePrimary()
	if err != nil {
		return 0, err
	}
	for p.pos < len(p.input) {
		op := p.input[p.pos]
		if op != '*' && op != '/' {
			return value, nil
		}
		p.pos++
		right, err := p.parsePrimary()
		if err != nil {
			return 0, err
		}
		if op == '*' {
			value *= right
			continue
		}
		if right == 0 {
			return 0, ErrDivideByZero
		}
		value /= right
	}
	return value, nil
}

func (p *parser) parsePrimary() (int, error) {
	start := p.pos
	if p.pos >= len(p.input) {
		return 0, ErrInvalidExpression
	}
	if p.input[p.pos] == '(' {
		p.pos++
		value, err := p.parseSum()
		if err != nil {
			return 0, err
		}
		if p.pos >= len(p.input) || p.input[p.pos] != ')' {
			return 0, ErrInvalidExpression
		}
		p.pos++
		return value, nil
	}
	for p.pos < len(p.input) && unicode.IsDigit(rune(p.input[p.pos])) {
		p.pos++
	}
	if p.pos == start {
		return 0, ErrInvalidExpression
	}
	value, err := strconv.Atoi(p.input[start:p.pos])
	if err != nil {
		return 0, ErrInvalidExpression
	}
	return value, nil
}
