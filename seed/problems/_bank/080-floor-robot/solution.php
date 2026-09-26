<?php
function robot_position(string $commands): array {
    $x = 0; $y = 0; $dx = 0; $dy = 1;
    $n = strlen($commands);
    for ($i = 0; $i < $n; $i++) {
        $c = $commands[$i];
        if ($c === 'F') {
            $x += $dx;
            $y += $dy;
        } elseif ($c === 'L') {
            [$dx, $dy] = [-$dy, $dx];
        } elseif ($c === 'R') {
            [$dx, $dy] = [$dy, -$dx];
        }
    }
    return [$x, $y];
}
