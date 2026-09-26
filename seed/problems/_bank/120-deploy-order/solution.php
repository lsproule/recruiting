<?php
function deploy_order(array $services, array $deps): array {
    $indegree = [];
    $after = [];
    foreach ($services as $s) {
        $indegree[$s] = 0;
        $after[$s] = [];
    }
    foreach ($deps as [$before, $then]) {
        $after[$before][] = $then;
        $indegree[$then]++;
    }
    $ready = new SplMinHeap();
    foreach ($services as $s) {
        if ($indegree[$s] === 0) {
            $ready->insert($s);
        }
    }
    $order = [];
    while (!$ready->isEmpty()) {
        $s = $ready->extract();
        $order[] = $s;
        foreach ($after[$s] as $t) {
            if (--$indegree[$t] === 0) {
                $ready->insert($t);
            }
        }
    }
    return count($order) === count($services) ? $order : [];
}
