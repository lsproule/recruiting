use std::collections::VecDeque;

fn shortest_route(grid: Vec<Vec<i32>>) -> i32 {
    let (rows, cols) = (grid.len(), grid[0].len());
    if grid[0][0] != 0 || grid[rows - 1][cols - 1] != 0 {
        return -1;
    }
    let mut dist = vec![vec![-1i32; cols]; rows];
    dist[0][0] = 0;
    let mut queue = VecDeque::new();
    queue.push_back((0usize, 0usize));
    let moves: [(i32, i32); 4] = [(1, 0), (-1, 0), (0, 1), (0, -1)];
    while let Some((r, c)) = queue.pop_front() {
        if r == rows - 1 && c == cols - 1 {
            return dist[r][c];
        }
        for (dr, dc) in moves {
            let (nr, nc) = (r as i32 + dr, c as i32 + dc);
            if nr < 0 || nc < 0 || nr as usize >= rows || nc as usize >= cols {
                continue;
            }
            let (nr, nc) = (nr as usize, nc as usize);
            if grid[nr][nc] == 0 && dist[nr][nc] < 0 {
                dist[nr][nc] = dist[r][c] + 1;
                queue.push_back((nr, nc));
            }
        }
    }
    -1
}
