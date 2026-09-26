use std::collections::VecDeque;

fn dropped_requests(timestamps: Vec<i64>, limit: i32, window: i64) -> i32 {
    let mut accepted: VecDeque<i64> = VecDeque::new();
    let mut dropped = 0;
    for t in timestamps {
        while accepted.front().map_or(false, |&f| f <= t - window) {
            accepted.pop_front();
        }
        if (accepted.len() as i32) < limit {
            accepted.push_back(t);
        } else {
            dropped += 1;
        }
    }
    dropped
}
