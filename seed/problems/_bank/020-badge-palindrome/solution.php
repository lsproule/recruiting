<?php
function is_badge_palindrome(string $badge): bool {
    $keep = strtolower(preg_replace('/[^a-z0-9]/i', '', $badge));
    return $keep === strrev($keep);
}
