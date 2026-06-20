package glossary

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// Term is a single glossary mapping. Locked terms are enforced exactly.
type Term struct {
	EN     string `json:"term_en"`
	FR     string `json:"term_fr"`
	Locked bool   `json:"locked"`
}

// Glossary holds locked term mappings, ordered longest-EN-first so that longer
// terms match before their prefixes.
type Glossary struct {
	locked   []Term
	patterns []*regexp.Regexp // aligned with locked, whole-word matchers
}

// New builds a glossary from terms, keeping only locked entries (V1 scope).
func New(terms []Term) (*Glossary, error) {
	locked := make([]Term, 0, len(terms))
	seen := make(map[string]string, len(terms))
	for _, t := range terms {
		if !t.Locked {
			continue
		}
		if t.EN == "" {
			return nil, ErrEmptyTermEN
		}
		if t.FR == "" {
			return nil, ErrEmptyTermFR
		}
		if prev, ok := seen[t.EN]; ok {
			if prev != t.FR {
				return nil, fmt.Errorf("unable to load glossary: %q -> %q and %q, err: %w", t.EN, prev, t.FR, ErrConflictingTerm)
			}
			continue // exact duplicate; keep a single pattern
		}
		seen[t.EN] = t.FR
		locked = append(locked, t)
	}

	sort.SliceStable(locked, func(i, j int) bool {
		return len(locked[i].EN) > len(locked[j].EN)
	})

	patterns := make([]*regexp.Regexp, len(locked))
	for i, t := range locked {
		// Case-sensitive whole-word match; proper nouns are case-sensitive and
		// this also avoids matching inside lowercase placeholder tokens.
		patterns[i] = regexp.MustCompile(`\b` + regexp.QuoteMeta(t.EN) + `\b`)
	}

	return &Glossary{locked: locked, patterns: patterns}, nil
}

// Load reads a JSON array of terms from path and builds a glossary.
func Load(path string) (*Glossary, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("unable to read glossary %q, err: %w", path, err)
	}

	var terms []Term
	if err := json.Unmarshal(data, &terms); err != nil {
		return nil, fmt.Errorf("unable to parse glossary %q, err: %w", path, err)
	}

	return New(terms)
}

const glossaryTokenPrefix = "<g id=\""

func glossaryToken(id int) string {
	return fmt.Sprintf(`<g id="%d"/>`, id)
}

// Protect replaces each locked source term in text with an opaque token and
// returns the protected text plus a token->canonical-target substitution map.
func (g *Glossary) Protect(text string) (string, map[string]string) {
	subs := make(map[string]string)
	if g == nil {
		return text, subs
	}

	id := 0
	for i, pattern := range g.patterns {
		fr := g.locked[i].FR
		text = pattern.ReplaceAllStringFunc(text, func(string) string {
			id++
			token := glossaryToken(id)
			subs[token] = fr
			return token
		})
	}

	return text, subs
}

// Apply substitutes glossary tokens in translated text with their canonical
// target terms.
func (g *Glossary) Apply(text string, subs map[string]string) string {
	for token, fr := range subs {
		text = strings.ReplaceAll(text, token, fr)
	}
	return text
}

// Terms returns the locked glossary terms in matching order.
func (g *Glossary) Terms() []Term {
	if g == nil {
		return nil
	}
	terms := make([]Term, len(g.locked))
	copy(terms, g.locked)
	return terms
}

// Violations counts locked terms present in source whose canonical target term
// is absent from the (post-enforcement) target. It is a safety-net QA metric;
// with Protect/Apply it should always be zero.
func (g *Glossary) Violations(source, target string) int {
	if g == nil {
		return 0
	}

	count := 0
	for i, term := range g.locked {
		if g.patterns[i].MatchString(source) && !strings.Contains(target, term.FR) {
			count++
		}
	}
	return count
}

// HasGlossaryTokens reports whether text still contains unresolved glossary
// tokens (useful for diagnostics).
func HasGlossaryTokens(text string) bool {
	return strings.Contains(text, glossaryTokenPrefix)
}
