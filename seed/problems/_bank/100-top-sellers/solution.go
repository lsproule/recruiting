package main

import "sort"

func top_sellers(sales map[string]int, k int) []string {
	names := make([]string, 0, len(sales))
	for name := range sales {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if sales[names[i]] != sales[names[j]] {
			return sales[names[i]] > sales[names[j]]
		}
		return names[i] < names[j]
	})
	if k < len(names) {
		names = names[:k]
	}
	return names
}
