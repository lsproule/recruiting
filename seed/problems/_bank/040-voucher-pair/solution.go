package main

func voucher_pair(amounts []int, target int) []int {
	seen := make(map[int]int, len(amounts))
	for j, a := range amounts {
		if i, ok := seen[target-a]; ok {
			return []int{i, j}
		}
		if _, ok := seen[a]; !ok {
			seen[a] = j
		}
	}
	return []int{}
}
