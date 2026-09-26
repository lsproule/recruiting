package main

func shortest_route(grid [][]int) int {
	rows, cols := len(grid), len(grid[0])
	if grid[0][0] != 0 || grid[rows-1][cols-1] != 0 {
		return -1
	}
	dist := make([][]int, rows)
	for r := range dist {
		dist[r] = make([]int, cols)
		for c := range dist[r] {
			dist[r][c] = -1
		}
	}
	dist[0][0] = 0
	queue := [][2]int{{0, 0}}
	for head := 0; head < len(queue); head++ {
		r, c := queue[head][0], queue[head][1]
		if r == rows-1 && c == cols-1 {
			return dist[r][c]
		}
		for _, d := range [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
			nr, nc := r+d[0], c+d[1]
			if nr >= 0 && nr < rows && nc >= 0 && nc < cols && grid[nr][nc] == 0 && dist[nr][nc] < 0 {
				dist[nr][nc] = dist[r][c] + 1
				queue = append(queue, [2]int{nr, nc})
			}
		}
	}
	return -1
}
