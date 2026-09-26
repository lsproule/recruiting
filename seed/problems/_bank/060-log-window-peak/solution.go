package main

func peak_window(counts []int, width int) int64 {
	var current int64
	for i := 0; i < width; i++ {
		current += int64(counts[i])
	}
	best := current
	for i := width; i < len(counts); i++ {
		current += int64(counts[i]) - int64(counts[i-width])
		if current > best {
			best = current
		}
	}
	return best
}
