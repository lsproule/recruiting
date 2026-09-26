fn min_coins(coins: Vec<i32>, amount: i32) -> i32 {
    let amount = amount as usize;
    let unreachable = amount + 1;
    let mut best = vec![unreachable; amount + 1];
    best[0] = 0;
    for a in 1..=amount {
        for &c in &coins {
            let c = c as usize;
            if c <= a && best[a - c] + 1 < best[a] {
                best[a] = best[a - c] + 1;
            }
        }
    }
    if best[amount] == unreachable { -1 } else { best[amount] as i32 }
}
