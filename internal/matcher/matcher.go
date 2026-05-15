// Package matcher finds equivalent markets across Polymarket and Kalshi
// using normalized text similarity (Levenshtein ratio via pure-Go impl).
package matcher

import (
	"strings"
	"unicode"

	"github.com/sandswind/polybot/internal/model"
)

// MinScore is the minimum fuzzy-match score [0,100] to consider two markets
// the same event. Tune upward to reduce false positives.
const MinScore = 72.0

// MarketPair holds two matched markets from different platforms.
type MarketPair struct {
	Poly   model.Market
	Kalshi model.Market
	Score  float64
}

// Match pairs every Polymarket market with its best-matching Kalshi market.
// Only pairs whose similarity score >= MinScore are returned.
func Match(polyMarkets, kalshiMarkets []model.Market) []MarketPair {
	var pairs []MarketPair

	for _, pm := range polyMarkets {
		pNorm := normalizeQuestion(pm.Question)

		bestScore := 0.0
		var bestKalshi model.Market

		for _, km := range kalshiMarkets {
			kNorm := normalizeQuestion(km.Question)
			score := similarity(pNorm, kNorm)
			if score > bestScore {
				bestScore = score
				bestKalshi = km
			}
		}

		if bestScore >= MinScore {
			pairs = append(pairs, MarketPair{
				Poly:   pm,
				Kalshi: bestKalshi,
				Score:  bestScore,
			})
		}
	}
	return pairs
}

// normalizeQuestion lowercases, removes punctuation and common filler words
// so that "Will France win the 2026 World Cup?" and
// "France wins 2026 FIFA World Cup" score highly.
func normalizeQuestion(q string) string {
	q = strings.ToLower(q)

	// remove punctuation
	q = strings.Map(func(r rune) rune {
		if unicode.IsPunct(r) {
			return ' '
		}
		return r
	}, q)

	// remove stop words
	stop := map[string]bool{
		"will": true, "the": true, "a": true, "an": true, "be": true,
		"to": true, "in": true, "of": true, "and": true, "or": true,
		"is": true, "by": true, "at": true, "it": true, "for": true,
		"on": true, "win": true, "wins": true, "winning": true,
	}

	words := strings.Fields(q)
	kept := words[:0]
	for _, w := range words {
		if !stop[w] {
			kept = append(kept, w)
		}
	}
	return strings.Join(kept, " ")
}

// similarity returns a score in [0, 100] using the Sørensen–Dice coefficient
// over character bigrams — fast, no external deps, works well for short titles.
func similarity(a, b string) float64 {
	if a == b {
		return 100
	}
	if len(a) == 0 || len(b) == 0 {
		return 0
	}

	bigramsA := bigrams(a)
	bigramsB := bigrams(b)

	if len(bigramsA) == 0 || len(bigramsB) == 0 {
		// fall back to word-overlap ratio
		return wordOverlap(a, b)
	}

	// count intersection
	intersection := 0
	freq := make(map[string]int, len(bigramsA))
	for _, bg := range bigramsA {
		freq[bg]++
	}
	for _, bg := range bigramsB {
		if freq[bg] > 0 {
			intersection++
			freq[bg]--
		}
	}

	dice := float64(2*intersection) / float64(len(bigramsA)+len(bigramsB))
	return dice * 100
}

func bigrams(s string) []string {
	runes := []rune(s)
	if len(runes) < 2 {
		return nil
	}
	result := make([]string, 0, len(runes)-1)
	for i := 0; i < len(runes)-1; i++ {
		result = append(result, string(runes[i:i+2]))
	}
	return result
}

func wordOverlap(a, b string) float64 {
	setA := toSet(strings.Fields(a))
	setB := toSet(strings.Fields(b))

	inter := 0
	for w := range setA {
		if setB[w] {
			inter++
		}
	}
	union := len(setA) + len(setB) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union) * 100
}

func toSet(words []string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}
