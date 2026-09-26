use std::collections::HashMap;

fn word_tally(words: Vec<String>) -> HashMap<String, i32> {
    let mut counts = HashMap::new();
    for w in words {
        *counts.entry(w).or_insert(0) += 1;
    }
    counts
}
