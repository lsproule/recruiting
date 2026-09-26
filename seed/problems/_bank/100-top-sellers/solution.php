<?php
function top_sellers(array $sales, int $k): array {
    $names = array_keys($sales);
    usort($names, function ($a, $b) use ($sales) {
        return $sales[$b] <=> $sales[$a] ?: strcmp($a, $b);
    });
    return array_slice($names, 0, $k);
}
