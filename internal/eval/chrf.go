package eval

import "strings"

const (
	// DefaultOrder is the maximum character n-gram length (sacrebleu-style).
	DefaultOrder = 6
	// DefaultBeta weights recall beta× as much as precision (chrF standard).
	DefaultBeta = 2.0
)

// ChrF computes a chrF-style character n-gram F-score in [0,1] between a
// candidate and a reference. Whitespace is collapsed; n-grams run 1..order and
// precision/recall are averaged across orders, then combined with a
// beta-weighted F-score. order<=0 and beta<=0 fall back to defaults.
func ChrF(candidate, reference string, order int, beta float64) float64 {
	if order <= 0 {
		order = DefaultOrder
	}
	if beta <= 0 {
		beta = DefaultBeta
	}

	cand := []rune(collapseWhitespace(candidate))
	ref := []rune(collapseWhitespace(reference))

	if len(cand) == 0 && len(ref) == 0 {
		return 1
	}

	var sumP, sumR float64
	orders := 0
	for n := 1; n <= order; n++ {
		cg := charNGrams(cand, n)
		rg := charNGrams(ref, n)
		if len(cg) == 0 && len(rg) == 0 {
			continue
		}

		match := 0
		for gram, c := range cg {
			if r, ok := rg[gram]; ok {
				match += min(c, r)
			}
		}

		totalC := sumCounts(cg)
		totalR := sumCounts(rg)
		var p, r float64
		if totalC > 0 {
			p = float64(match) / float64(totalC)
		}
		if totalR > 0 {
			r = float64(match) / float64(totalR)
		}
		sumP += p
		sumR += r
		orders++
	}

	if orders == 0 {
		return 0
	}
	avgP := sumP / float64(orders)
	avgR := sumR / float64(orders)
	b2 := beta * beta
	denom := b2*avgP + avgR
	if denom == 0 {
		return 0
	}
	return (1 + b2) * avgP * avgR / denom
}

func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func charNGrams(runes []rune, n int) map[string]int {
	grams := make(map[string]int)
	if len(runes) < n {
		return grams
	}
	for i := 0; i+n <= len(runes); i++ {
		grams[string(runes[i:i+n])]++
	}
	return grams
}

func sumCounts(m map[string]int) int {
	total := 0
	for _, c := range m {
		total += c
	}
	return total
}
