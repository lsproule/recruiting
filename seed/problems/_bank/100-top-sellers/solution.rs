use std::collections::HashMap;

fn top_sellers(sales: HashMap<String, i32>, k: i32) -> Vec<String> {
    let mut names: Vec<(String, i32)> = sales.into_iter().collect();
    names.sort_by(|a, b| b.1.cmp(&a.1).then_with(|| a.0.cmp(&b.0)));
    names.into_iter().take(k.max(0) as usize).map(|(name, _)| name).collect()
}
