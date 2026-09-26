package main

import "container/heap"

type minStrings []string

func (h minStrings) Len() int           { return len(h) }
func (h minStrings) Less(i, j int) bool { return h[i] < h[j] }
func (h minStrings) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *minStrings) Push(x any)        { *h = append(*h, x.(string)) }
func (h *minStrings) Pop() any          { old := *h; x := old[len(old)-1]; *h = old[:len(old)-1]; return x }

func deploy_order(services []string, deps [][]string) []string {
	indegree := make(map[string]int, len(services))
	after := make(map[string][]string, len(services))
	for _, s := range services {
		indegree[s] = 0
	}
	for _, d := range deps {
		after[d[0]] = append(after[d[0]], d[1])
		indegree[d[1]]++
	}
	ready := &minStrings{}
	for _, s := range services {
		if indegree[s] == 0 {
			*ready = append(*ready, s)
		}
	}
	heap.Init(ready)
	order := make([]string, 0, len(services))
	for ready.Len() > 0 {
		s := heap.Pop(ready).(string)
		order = append(order, s)
		for _, t := range after[s] {
			indegree[t]--
			if indegree[t] == 0 {
				heap.Push(ready, t)
			}
		}
	}
	if len(order) != len(services) {
		return []string{}
	}
	return order
}
