package main

func receipt_total(quantities []int, prices []int) int64 {
	var total int64
	for i, q := range quantities {
		total += int64(q) * int64(prices[i])
	}
	return total
}
