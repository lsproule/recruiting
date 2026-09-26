<?php
function moving_average(array $readings, int $width): array {
    $total = 0.0;
    for ($i = 0; $i < $width; $i++) {
        $total += $readings[$i];
    }
    $out = [$total / $width];
    $n = count($readings);
    for ($i = $width; $i < $n; $i++) {
        $total += $readings[$i] - $readings[$i - $width];
        $out[] = $total / $width;
    }
    return $out;
}
