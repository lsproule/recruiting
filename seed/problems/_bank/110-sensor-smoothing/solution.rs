fn moving_average(readings: Vec<f64>, width: i32) -> Vec<f64> {
    let w = width as usize;
    let mut total: f64 = readings.iter().take(w).sum();
    let mut out = vec![total / w as f64];
    for i in w..readings.len() {
        total += readings[i] - readings[i - w];
        out.push(total / w as f64);
    }
    out
}
