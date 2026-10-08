package agent

import (
	"context"
	"sort"
	"strings"
	"unicode"
)

// LexicalToolDiscovery provides a deterministic, stateless, read-only default
// implementation of ToolDiscovery for authorized local/MCP Tools. It does not
// build a second catalog, persist an index or contact an external service.
// Production quality/recall must be measured before larger rollout.
type LexicalToolDiscovery struct{}

func (LexicalToolDiscovery) Search(ctx context.Context, request ToolDiscoveryRequest) ([]string, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if request.RunID == "" || len(request.Candidates) == 0 ||
		len(request.Definitions) != len(request.Candidates) ||
		request.MaxResults <= 0 || request.MaxResults > discoveryMaxResults {
		return nil, ErrToolDiscoveryInvalid
	}
	terms := lexicalDiscoveryTerms(request.Query)
	if len(terms) == 0 {
		return nil, ErrToolDiscoveryInvalid
	}
	authorized := make(map[string]struct{}, len(request.Candidates))
	for _, candidate := range request.Candidates {
		if candidate.Key == "" || candidate.Fingerprint == "" {
			return nil, ErrToolDiscoveryInvalid
		}
		authorized[candidate.Key] = struct{}{}
	}
	type ranked struct {
		key   string
		score int
	}
	scores := make([]ranked, 0, len(request.Definitions))
	seen := make(map[string]struct{}, len(request.Definitions))
	for _, definition := range request.Definitions {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, ok := authorized[definition.Key]; !ok {
			return nil, ErrToolDiscoveryDenied
		}
		if _, duplicate := seen[definition.Key]; duplicate {
			return nil, ErrToolDiscoveryInvalid
		}
		seen[definition.Key] = struct{}{}
		key := strings.ToLower(definition.Key)
		name := strings.ToLower(definition.Name)
		description := strings.ToLower(definition.Description)
		score := 0
		for _, term := range terms {
			switch {
			case strings.Contains(name, term):
				score += 8
			case strings.Contains(key, term):
				score += 6
			case strings.Contains(description, term):
				score += 3
			}
		}
		if score > 0 {
			scores = append(scores, ranked{key: definition.Key, score: score})
		}
	}
	sort.Slice(scores, func(i, j int) bool {
		if scores[i].score != scores[j].score {
			return scores[i].score > scores[j].score
		}
		return scores[i].key < scores[j].key
	})
	if len(scores) > request.MaxResults {
		scores = scores[:request.MaxResults]
	}
	result := make([]string, 0, len(scores))
	for _, ranked := range scores {
		result = append(result, ranked.key)
	}
	return result, nil
}

func lexicalDiscoveryTerms(query string) []string {
	normalized := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return ' '
	}, query)
	terms := strings.Fields(normalized)
	if len(terms) > 32 {
		terms = terms[:32]
	}
	seen := make(map[string]struct{}, len(terms))
	result := make([]string, 0, len(terms))
	for _, term := range terms {
		if len(term) > 64 {
			continue
		}
		if _, duplicate := seen[term]; duplicate {
			continue
		}
		seen[term] = struct{}{}
		result = append(result, term)
	}
	return result
}
