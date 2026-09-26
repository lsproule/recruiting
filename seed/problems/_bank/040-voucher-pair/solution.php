<?php
function voucher_pair(array $amounts, int $target): array {
    $seen = [];
    foreach ($amounts as $j => $a) {
        if (isset($seen[$target - $a])) {
            return [$seen[$target - $a], $j];
        }
        if (!isset($seen[$a])) {
            $seen[$a] = $j;
        }
    }
    return [];
}
