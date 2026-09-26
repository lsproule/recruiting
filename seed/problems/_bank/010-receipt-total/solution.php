<?php
function receipt_total(array $quantities, array $prices): int {
    $total = 0;
    foreach ($quantities as $i => $q) {
        $total += $q * $prices[$i];
    }
    return $total;
}
