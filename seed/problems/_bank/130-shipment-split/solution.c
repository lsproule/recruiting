static int fits(const int *weights, int n, int trucks, int capacity) {
    int used = 1, load = 0;
    for (int i = 0; i < n; i++) {
        if (load + weights[i] > capacity) {
            used++;
            load = weights[i];
        } else {
            load += weights[i];
        }
    }
    return used <= trucks;
}

int min_max_load(const int *weights, int weights_len, int trucks) {
    int lo = 0, hi = 0;
    for (int i = 0; i < weights_len; i++) {
        if (weights[i] > lo) lo = weights[i];
        hi += weights[i];
    }
    while (lo < hi) {
        int mid = lo + (hi - lo) / 2;
        if (fits(weights, weights_len, trucks, mid)) hi = mid; else lo = mid + 1;
    }
    return lo;
}
