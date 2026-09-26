from collections import deque


def shortest_route(grid: list[list[int]]) -> int:
    rows, cols = len(grid), len(grid[0])
    if grid[0][0] or grid[rows - 1][cols - 1]:
        return -1
    dist = [[-1] * cols for _ in range(rows)]
    dist[0][0] = 0
    queue = deque([(0, 0)])
    while queue:
        r, c = queue.popleft()
        if r == rows - 1 and c == cols - 1:
            return dist[r][c]
        for nr, nc in ((r + 1, c), (r - 1, c), (r, c + 1), (r, c - 1)):
            if 0 <= nr < rows and 0 <= nc < cols and grid[nr][nc] == 0 and dist[nr][nc] < 0:
                dist[nr][nc] = dist[r][c] + 1
                queue.append((nr, nc))
    return -1
