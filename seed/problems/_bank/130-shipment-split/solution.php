<?php
function min_max_load(array $weights, int $trucks): int {
    $fits = function (int $capacity) use ($weights, $trucks): bool {
        $used = 1;
        $load = 0;
        foreach ($weights as $w) {
            if ($load + $w > $capacity) {
                $used++;
                $load = $w;
            } else {
                $load += $w;
            }
        }
        return $used <= $trucks;
    };
    $lo = max($weights);
    $hi = array_sum($weights);
    while ($lo < $hi) {
        $mid = intdiv($lo + $hi, 2);
        if ($fits($mid)) {
            $hi = $mid;
        } else {
            $lo = $mid + 1;
        }
    }
    return $lo;
}
