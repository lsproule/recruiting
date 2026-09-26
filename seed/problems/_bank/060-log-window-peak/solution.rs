fn peak_window(counts: Vec<i32>, width: i32) -> i64 {
    let w = width as usize;
    let mut current: i64 = counts.iter().take(w).map(|&c| c as i64).sum();
    let mut best = current;
    for i in w..counts.len() {
        current += counts[i] as i64 - counts[i - w] as i64;
        best = best.max(current);
    }
    best
}
