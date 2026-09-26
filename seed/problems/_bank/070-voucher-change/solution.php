<?php
function min_coins(array $coins, int $amount): int {
    $unreachable = $amount + 1;
    $best = array_fill(0, $amount + 1, $unreachable);
    $best[0] = 0;
    for ($a = 1; $a <= $amount; $a++) {
        foreach ($coins as $c) {
            if ($c <= $a && $best[$a - $c] + 1 < $best[$a]) {
                $best[$a] = $best[$a - $c] + 1;
            }
        }
    }
    return $best[$amount] === $unreachable ? -1 : $best[$amount];
}
