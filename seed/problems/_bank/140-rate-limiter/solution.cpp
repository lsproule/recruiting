#include <deque>
#include <vector>

int dropped_requests(std::vector<long long> timestamps, int limit, long long window) {
    std::deque<long long> accepted;
    int dropped = 0;
    for (long long t : timestamps) {
        while (!accepted.empty() && accepted.front() <= t - window) accepted.pop_front();
        if (static_cast<int>(accepted.size()) < limit) accepted.push_back(t); else dropped++;
    }
    return dropped;
}
