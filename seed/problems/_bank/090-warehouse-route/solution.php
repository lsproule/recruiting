<?php
function shortest_route(array $grid): int {
    $rows = count($grid);
    $cols = count($grid[0]);
    if ($grid[0][0] === 1 || $grid[$rows - 1][$cols - 1] === 1) {
        return -1;
    }
    $dist = array_fill(0, $rows, array_fill(0, $cols, -1));
    $dist[0][0] = 0;
    $queue = [[0, 0]];
    $head = 0;
    while ($head < count($queue)) {
        [$r, $c] = $queue[$head++];
        if ($r === $rows - 1 && $c === $cols - 1) {
            return $dist[$r][$c];
        }
        foreach ([[$r + 1, $c], [$r - 1, $c], [$r, $c + 1], [$r, $c - 1]] as [$nr, $nc]) {
            if ($nr >= 0 && $nr < $rows && $nc >= 0 && $nc < $cols && $grid[$nr][$nc] === 0 && $dist[$nr][$nc] < 0) {
                $dist[$nr][$nc] = $dist[$r][$c] + 1;
                $queue[] = [$nr, $nc];
            }
        }
    }
    return -1;
}
