<?php
function peak_window(array $counts, int $width): int {
    $current = 0;
    for ($i = 0; $i < $width; $i++) {
        $current += $counts[$i];
    }
    $best = $current;
    $n = count($counts);
    for ($i = $width; $i < $n; $i++) {
        $current += $counts[$i] - $counts[$i - $width];
        if ($current > $best) {
            $best = $current;
        }
    }
    return $best;
}
