package main

func min_coins(coins []int, amount int) int {
	unreachable := amount + 1
	best := make([]int, amount+1)
	for a := 1; a <= amount; a++ {
		best[a] = unreachable
		for _, c := range coins {
			if c <= a && best[a-c]+1 < best[a] {
				best[a] = best[a-c] + 1
			}
		}
	}
	if best[amount] == unreachable {
		return -1
	}
	return best[amount]
}
