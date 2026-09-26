package main

import "sort"

func rooms_needed(starts []int, ends []int) int {
	s := append([]int(nil), starts...)
	e := append([]int(nil), ends...)
	sort.Ints(s)
	sort.Ints(e)
	rooms, best, j := 0, 0, 0
	for _, start := range s {
		for j < len(e) && e[j] <= start {
			rooms--
			j++
		}
		rooms++
		if rooms > best {
			best = rooms
		}
	}
	return best
}
