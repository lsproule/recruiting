package main

func min_max_load(weights []int, trucks int) int {
	fits := func(capacity int) bool {
		used, load := 1, 0
		for _, w := range weights {
			if load+w > capacity {
				used++
				load = w
			} else {
				load += w
			}
		}
		return used <= trucks
	}
	lo, hi := 0, 0
	for _, w := range weights {
		if w > lo {
			lo = w
		}
		hi += w
	}
	for lo < hi {
		mid := (lo + hi) / 2
		if fits(mid) {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	return lo
}
