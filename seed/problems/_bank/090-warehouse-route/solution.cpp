#include <queue>
#include <utility>
#include <vector>

int shortest_route(std::vector<std::vector<int>> grid) {
    int rows = grid.size(), cols = grid[0].size();
    if (grid[0][0] != 0 || grid[rows - 1][cols - 1] != 0) return -1;
    std::vector<std::vector<int>> dist(rows, std::vector<int>(cols, -1));
    dist[0][0] = 0;
    std::queue<std::pair<int, int>> queue;
    queue.push({0, 0});
    const int moves[4][2] = {{1, 0}, {-1, 0}, {0, 1}, {0, -1}};
    while (!queue.empty()) {
        auto [r, c] = queue.front();
        queue.pop();
        if (r == rows - 1 && c == cols - 1) return dist[r][c];
        for (const auto& m : moves) {
            int nr = r + m[0], nc = c + m[1];
            if (nr >= 0 && nr < rows && nc >= 0 && nc < cols && grid[nr][nc] == 0 && dist[nr][nc] < 0) {
                dist[nr][nc] = dist[r][c] + 1;
                queue.push({nr, nc});
            }
        }
    }
    return -1;
}
