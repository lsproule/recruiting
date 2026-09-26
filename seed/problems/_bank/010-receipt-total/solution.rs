fn receipt_total(quantities: Vec<i32>, prices: Vec<i32>) -> i64 {
    quantities.iter().zip(prices.iter()).map(|(q, p)| *q as i64 * *p as i64).sum()
}
