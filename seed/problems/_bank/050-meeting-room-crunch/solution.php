<?php
function rooms_needed(array $starts, array $ends): int {
    sort($starts);
    sort($ends);
    $rooms = 0;
    $best = 0;
    $j = 0;
    $n = count($ends);
    foreach ($starts as $s) {
        while ($j < $n && $ends[$j] <= $s) {
            $rooms--;
            $j++;
        }
        $rooms++;
        if ($rooms > $best) {
            $best = $rooms;
        }
    }
    return $best;
}
