import java.util.*;

class Solution {
    public static int shortest_route(int[][] grid) {
        int rows = grid.length, cols = grid[0].length;
        if (grid[0][0] != 0 || grid[rows - 1][cols - 1] != 0) return -1;
        int[][] dist = new int[rows][cols];
        for (int[] row : dist) Arrays.fill(row, -1);
        dist[0][0] = 0;
        ArrayDeque<int[]> queue = new ArrayDeque<>();
        queue.add(new int[] {0, 0});
        int[][] moves = {{1, 0}, {-1, 0}, {0, 1}, {0, -1}};
        while (!queue.isEmpty()) {
            int[] cur = queue.poll();
            int r = cur[0], c = cur[1];
            if (r == rows - 1 && c == cols - 1) return dist[r][c];
            for (int[] m : moves) {
                int nr = r + m[0], nc = c + m[1];
                if (nr >= 0 && nr < rows && nc >= 0 && nc < cols && grid[nr][nc] == 0 && dist[nr][nc] < 0) {
                    dist[nr][nc] = dist[r][c] + 1;
                    queue.add(new int[] {nr, nc});
                }
            }
        }
        return -1;
    }
}
