fn min_max_load(weights: Vec<i32>, trucks: i32) -> i32 {
    let fits = |capacity: i32| {
        let (mut used, mut load) = (1, 0);
        for &w in &weights {
            if load + w > capacity {
                used += 1;
                load = w;
            } else {
                load += w;
            }
        }
        used <= trucks
    };
    let (mut lo, mut hi) = (*weights.iter().max().unwrap_or(&0), weights.iter().sum::<i32>());
    while lo < hi {
        let mid = lo + (hi - lo) / 2;
        if fits(mid) { hi = mid; } else { lo = mid + 1; }
    }
    lo
}
