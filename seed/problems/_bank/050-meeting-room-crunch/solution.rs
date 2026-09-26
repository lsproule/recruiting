fn rooms_needed(starts: Vec<i32>, ends: Vec<i32>) -> i32 {
    let mut s = starts;
    let mut e = ends;
    s.sort_unstable();
    e.sort_unstable();
    let (mut rooms, mut best, mut j) = (0, 0, 0);
    for &start in &s {
        while j < e.len() && e[j] <= start {
            rooms -= 1;
            j += 1;
        }
        rooms += 1;
        best = best.max(rooms);
    }
    best
}
