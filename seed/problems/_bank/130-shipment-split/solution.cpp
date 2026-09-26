#include <algorithm>
#include <vector>

static bool fits(const std::vector<int>& weights, int trucks, int capacity) {
    int used = 1, load = 0;
    for (int w : weights) {
        if (load + w > capacity) {
            used++;
            load = w;
        } else {
            load += w;
        }
    }
    return used <= trucks;
}

int min_max_load(std::vector<int> weights, int trucks) {
    int lo = 0, hi = 0;
    for (int w : weights) {
        lo = std::max(lo, w);
        hi += w;
    }
    while (lo < hi) {
        int mid = (lo + hi) / 2;
        if (fits(weights, trucks, mid)) hi = mid; else lo = mid + 1;
    }
    return lo;
}
