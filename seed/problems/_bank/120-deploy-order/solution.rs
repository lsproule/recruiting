use std::cmp::Reverse;
use std::collections::{BinaryHeap, HashMap};

fn deploy_order(services: Vec<String>, deps: Vec<Vec<String>>) -> Vec<String> {
    let mut indegree: HashMap<&str, usize> = services.iter().map(|s| (s.as_str(), 0)).collect();
    let mut after: HashMap<&str, Vec<&str>> = services.iter().map(|s| (s.as_str(), Vec::new())).collect();
    for d in &deps {
        after.entry(d[0].as_str()).or_default().push(d[1].as_str());
        *indegree.entry(d[1].as_str()).or_insert(0) += 1;
    }
    let mut ready: BinaryHeap<Reverse<&str>> = services
        .iter()
        .filter(|s| indegree[s.as_str()] == 0)
        .map(|s| Reverse(s.as_str()))
        .collect();
    let mut order: Vec<String> = Vec::with_capacity(services.len());
    while let Some(Reverse(s)) = ready.pop() {
        order.push(s.to_string());
        if let Some(list) = after.get(s) {
            for &t in list {
                let left = indegree.get_mut(t).unwrap();
                *left -= 1;
                if *left == 0 {
                    ready.push(Reverse(t));
                }
            }
        }
    }
    if order.len() == services.len() { order } else { Vec::new() }
}
