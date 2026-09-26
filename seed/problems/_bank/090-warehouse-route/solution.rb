def shortest_route(grid)
  rows = grid.length
  cols = grid[0].length
  return -1 if grid[0][0] == 1 || grid[rows - 1][cols - 1] == 1
  dist = Array.new(rows) { Array.new(cols, -1) }
  dist[0][0] = 0
  queue = [[0, 0]]
  head = 0
  while head < queue.length
    r, c = queue[head]
    head += 1
    return dist[r][c] if r == rows - 1 && c == cols - 1
    [[r + 1, c], [r - 1, c], [r, c + 1], [r, c - 1]].each do |nr, nc|
      next unless nr >= 0 && nr < rows && nc >= 0 && nc < cols
      next unless grid[nr][nc] == 0 && dist[nr][nc] < 0
      dist[nr][nc] = dist[r][c] + 1
      queue << [nr, nc]
    end
  end
  -1
end
