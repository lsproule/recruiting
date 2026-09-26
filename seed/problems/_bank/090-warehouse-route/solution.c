#include <stdlib.h>

int shortest_route(const int **grid, int grid_len, const int *grid_lens) {
    int rows = grid_len, cols = grid_lens[0];
    if (grid[0][0] != 0 || grid[rows - 1][cols - 1] != 0) return -1;
    int cells = rows * cols;
    int *dist = malloc(sizeof(int) * (size_t) cells);
    int *queue = malloc(sizeof(int) * (size_t) cells);
    for (int i = 0; i < cells; i++) dist[i] = -1;
    dist[0] = 0;
    int head = 0, tail = 0;
    queue[tail++] = 0;
    int answer = -1;
    const int moves[4][2] = {{1, 0}, {-1, 0}, {0, 1}, {0, -1}};
    while (head < tail) {
        int cur = queue[head++];
        int r = cur / cols, c = cur % cols;
        if (r == rows - 1 && c == cols - 1) { answer = dist[cur]; break; }
        for (int m = 0; m < 4; m++) {
            int nr = r + moves[m][0], nc = c + moves[m][1];
            if (nr < 0 || nr >= rows || nc < 0 || nc >= cols) continue;
            int next = nr * cols + nc;
            if (grid[nr][nc] == 0 && dist[next] < 0) {
                dist[next] = dist[cur] + 1;
                queue[tail++] = next;
            }
        }
    }
    free(dist);
    free(queue);
    return answer;
}
