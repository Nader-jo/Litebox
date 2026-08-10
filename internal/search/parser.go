// Package search parses Litebox's intentionally small search language.
package search

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Query is a validated search expression split into FTS and SQL-safe filters.
type Query struct {
	Terms         []string
	From          string
	To            string
	Subject       string
	HasAttachment bool
	Unread        bool
	Starred       bool
	After         *time.Time
	Before        *time.Time
}

// Parse validates structured operators and keeps unknown colons as literal terms.
func Parse(input string) (Query, error) {
	tokens, err := tokenize(input)
	if err != nil {
		return Query{}, err
	}
	var query Query
	for _, token := range tokens {
		name, value, hasOperator := strings.Cut(token, ":")
		if !hasOperator {
			query.Terms = append(query.Terms, token)
			continue
		}
		switch strings.ToLower(name) {
		case "from":
			query.From = value
		case "to":
			query.To = value
		case "subject":
			query.Subject = value
		case "has":
			if value != "attachment" {
				return Query{}, fmt.Errorf("unsupported has: filter %q", value)
			}
			query.HasAttachment = true
		case "is":
			switch value {
			case "unread":
				query.Unread = true
			case "starred":
				query.Starred = true
			default:
				return Query{}, fmt.Errorf("unsupported is: filter %q", value)
			}
		case "after", "before":
			date, err := time.Parse("2006-01-02", value)
			if err != nil {
				return Query{}, fmt.Errorf("%s: expects YYYY-MM-DD", name)
			}
			if name == "after" {
				query.After = &date
			} else {
				query.Before = &date
			}
		default:
			query.Terms = append(query.Terms, token)
		}
	}
	return query, nil
}

// FTS returns a safely quoted FTS5 expression.
func (q Query) FTS() string {
	quoted := make([]string, 0, len(q.Terms))
	for _, term := range q.Terms {
		term = strings.ReplaceAll(term, `"`, `""`)
		quoted = append(quoted, `"`+term+`"`)
	}
	return strings.Join(quoted, " AND ")
}

func tokenize(input string) ([]string, error) {
	var tokens []string
	var current strings.Builder
	quoted := false
	for index := 0; index < len(input); index++ {
		character := input[index]
		switch character {
		case '"':
			quoted = !quoted
		case '\\':
			if index+1 < len(input) && input[index+1] == '"' {
				index++
				current.WriteByte('"')
			} else {
				current.WriteByte(character)
			}
		case ' ', '\t', '\n', '\r':
			if quoted {
				current.WriteByte(character)
			} else if current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
		default:
			current.WriteByte(character)
		}
	}
	if quoted {
		return nil, fmt.Errorf("unterminated quoted phrase near %s", strconv.Quote(current.String()))
	}
	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}
	return tokens, nil
}
