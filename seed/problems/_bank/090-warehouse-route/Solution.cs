using System.Collections.Generic;

class Solution {
    public static int shortest_route(int[][] grid) {
        int rows = grid.Length, cols = grid[0].Length;
        if (grid[0][0] != 0 || grid[rows - 1][cols - 1] != 0) return -1;
        var dist = new int[rows, cols];
        for (int r = 0; r < rows; r++) for (int c = 0; c < cols; c++) dist[r, c] = -1;
        dist[0, 0] = 0;
        var queue = new Queue<(int, int)>();
        queue.Enqueue((0, 0));
        var moves = new[] { (1, 0), (-1, 0), (0, 1), (0, -1) };
        while (queue.Count > 0) {
            var (r, c) = queue.Dequeue();
            if (r == rows - 1 && c == cols - 1) return dist[r, c];
            foreach (var (dr, dc) in moves) {
                int nr = r + dr, nc = c + dc;
                if (nr >= 0 && nr < rows && nc >= 0 && nc < cols && grid[nr][nc] == 0 && dist[nr, nc] < 0) {
                    dist[nr, nc] = dist[r, c] + 1;
                    queue.Enqueue((nr, nc));
                }
            }
        }
        return -1;
    }
}
