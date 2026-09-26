<?php
function dropped_requests(array $timestamps, int $limit, int $window): int {
    $accepted = [];
    $head = 0;
    $dropped = 0;
    foreach ($timestamps as $t) {
        while ($head < count($accepted) && $accepted[$head] <= $t - $window) {
            $head++;
        }
        if (count($accepted) - $head < $limit) {
            $accepted[] = $t;
        } else {
            $dropped++;
        }
    }
    return $dropped;
}
