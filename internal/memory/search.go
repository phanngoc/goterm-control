package memory

import (
	"sort"
	"strings"
)

// searchEntries scores and returns the top-N entries matching the query.
func searchEntries(entries []Entry, query string, limit int) []Entry {
	if len(entries) == 0 || query == "" {
		return nil
	}

	queryTokens := tokenize(query)
	if len(queryTokens) == 0 {
		return nil
	}

	type scored struct {
		entry Entry
		score float64
	}

	// Minimum score threshold — prevents injecting weakly-matched memories
	// that share only generic words with the query.
	const minScore = 3.0

	var results []scored
	for _, e := range entries {
		s := scoreMatch(queryTokens, e)
		if s >= minScore {
			results = append(results, scored{entry: e, score: s})
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].score > results[j].score
	})

	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}

	out := make([]Entry, len(results))
	for i, r := range results {
		out[i] = r.entry
	}
	return out
}

// scoreMatch scores an entry against pre-tokenized query words.
func scoreMatch(queryTokens []string, entry Entry) float64 {
	var score float64

	// Keyword matches (highest weight)
	entryKeywords := make(map[string]bool, len(entry.Keywords))
	for _, kw := range entry.Keywords {
		entryKeywords[strings.ToLower(kw)] = true
	}
	for _, qt := range queryTokens {
		if entryKeywords[qt] {
			score += 2.0
		}
	}

	// Fact substring matches
	for _, fact := range entry.Facts {
		factLower := strings.ToLower(fact)
		for _, qt := range queryTokens {
			if strings.Contains(factLower, qt) {
				score += 1.0
			}
		}
	}

	// Summary substring match
	summaryLower := strings.ToLower(entry.Summary)
	for _, qt := range queryTokens {
		if strings.Contains(summaryLower, qt) {
			score += 0.5
		}
	}

	return score
}

// tokenize splits text into lowercase words, filtering stopwords and short words.
func tokenize(text string) []string {
	words := strings.Fields(strings.ToLower(text))
	var tokens []string
	for _, w := range words {
		w = strings.Trim(w, ".,!?;:\"'()[]{}")
		if len(w) < 3 || stopwords[w] {
			continue
		}
		tokens = append(tokens, w)
	}
	return tokens
}

// Stopwords merges English common words with the comprehensive Vietnamese
// stopword list (from github.com/stopwords/vietnamese-stopwords, 670 unique words).
// Exported for use by storage/memory.go FTS tokenizer.
var Stopwords = mergeStopwords(englishStopwords, vietnameseStopwords)

// keep internal alias for existing code
var stopwords = Stopwords

var englishStopwords = map[string]bool{
	"the": true, "and": true, "for": true, "are": true, "but": true,
	"not": true, "you": true, "all": true, "can": true, "had": true,
	"her": true, "was": true, "one": true, "our": true, "out": true,
	"has": true, "have": true, "been": true, "will": true, "with": true,
	"this": true, "that": true, "from": true, "they": true, "were": true,
	"what": true, "when": true, "make": true, "like": true, "how": true,
	"each": true, "she": true, "which": true, "their": true, "there": true,
	"about": true, "would": true, "these": true, "other": true, "into": true,
	"more": true, "some": true, "than": true, "them": true, "very": true,
}

func mergeStopwords(maps ...map[string]bool) map[string]bool {
	merged := make(map[string]bool)
	for _, m := range maps {
		for k := range m {
			merged[k] = true
		}
	}
	return merged
}
