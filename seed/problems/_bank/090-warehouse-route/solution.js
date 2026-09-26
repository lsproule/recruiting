/**
 * @param {number[][]} grid
 * @returns {number}
 */
function shortest_route(grid) {
  const rows = grid.length, cols = grid[0].length;
  if (grid[0][0] || grid[rows - 1][cols - 1]) return -1;
  const dist = grid.map((row) => row.map(() => -1));
  dist[0][0] = 0;
  const queue = [[0, 0]];
  let head = 0;
  while (head < queue.length) {
    const [r, c] = queue[head++];
    if (r === rows - 1 && c === cols - 1) return dist[r][c];
    for (const [nr, nc] of [[r + 1, c], [r - 1, c], [r, c + 1], [r, c - 1]]) {
      if (nr >= 0 && nr < rows && nc >= 0 && nc < cols && grid[nr][nc] === 0 && dist[nr][nc] < 0) {
        dist[nr][nc] = dist[r][c] + 1;
        queue.push([nr, nc]);
      }
    }
  }
  return -1;
}
