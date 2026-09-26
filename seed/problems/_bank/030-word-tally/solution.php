<?php
function word_tally(array $words): array {
    $counts = [];
    foreach ($words as $w) {
        $counts[$w] = ($counts[$w] ?? 0) + 1;
    }
    return $counts;
}
