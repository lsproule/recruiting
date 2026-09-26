fn is_badge_palindrome(badge: String) -> bool {
    let keep: Vec<char> = badge
        .chars()
        .filter(|c| c.is_ascii_alphanumeric())
        .map(|c| c.to_ascii_lowercase())
        .collect();
    keep.iter().eq(keep.iter().rev())
}
