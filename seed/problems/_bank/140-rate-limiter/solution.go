package main

func dropped_requests(timestamps []int64, limit int, window int64) int {
	accepted := make([]int64, 0, len(timestamps))
	head, dropped := 0, 0
	for _, t := range timestamps {
		for head < len(accepted) && accepted[head] <= t-window {
			head++
		}
		if len(accepted)-head < limit {
			accepted = append(accepted, t)
		} else {
			dropped++
		}
	}
	return dropped
}
