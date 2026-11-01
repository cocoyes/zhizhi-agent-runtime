package skill

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// Candidate contains only model-visible metadata, never filesystem paths.
type Candidate struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Fingerprint string `json:"fingerprint"`
}

// Searcher optionally supplies application-specific ranking without loading bodies.
type Searcher interface {
	Search(context.Context, string, int) ([]Candidate, error)
}

// Search ranks metadata deterministically with Latin terms and CJK bigrams.
func (c *Catalog) Search(ctx context.Context, query string, limit int) ([]Candidate, error) {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil, fmt.Errorf("skill: query is required")
	}
	if limit <= 0 {
		limit = 5
	}
	if limit > 10 {
		limit = 10
	}
	terms := strings.FieldsFunc(query, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	var tokens []string
	for _, term := range terms {
		r := []rune(term)
		if len(r) > 1 && unicode.Is(unicode.Han, r[0]) {
			for i := 0; i < len(r)-1; i++ {
				tokens = append(tokens, string(r[i:i+2]))
			}
		} else {
			tokens = append(tokens, term)
		}
	}
	type ranked struct {
		meta  Metadata
		score int
	}
	var values []ranked
	for _, meta := range c.Metadata() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name, description := strings.ToLower(meta.Name), strings.ToLower(meta.Description)
		score := 0
		if name == query {
			score += 10000
		}
		for _, term := range tokens {
			if strings.Contains(name, term) {
				score += 100
			}
			if strings.Contains(description, term) {
				score += 10
			}
		}
		if score > 0 {
			values = append(values, ranked{meta, score})
		}
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].score == values[j].score {
			return values[i].meta.Name < values[j].meta.Name
		}
		return values[i].score > values[j].score
	})
	if len(values) > limit {
		values = values[:limit]
	}
	out := make([]Candidate, 0, len(values))
	for _, v := range values {
		out = append(out, Candidate{Name: v.meta.Name, Description: v.meta.Description, Fingerprint: v.meta.Fingerprint})
	}
	return out, nil
}
