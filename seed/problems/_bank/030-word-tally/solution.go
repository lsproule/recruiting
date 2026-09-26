package main

func word_tally(words []string) map[string]int {
	counts := make(map[string]int, len(words))
	for _, w := range words {
		counts[w]++
	}
	return counts
}
