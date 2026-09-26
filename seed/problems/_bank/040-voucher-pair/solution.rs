use std::collections::HashMap;

fn voucher_pair(amounts: Vec<i32>, target: i32) -> Vec<i32> {
    let mut seen: HashMap<i32, i32> = HashMap::new();
    for (j, &a) in amounts.iter().enumerate() {
        if let Some(&i) = seen.get(&(target - a)) {
            return vec![i, j as i32];
        }
        seen.entry(a).or_insert(j as i32);
    }
    Vec::new()
}
