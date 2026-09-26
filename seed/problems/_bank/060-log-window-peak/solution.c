long long peak_window(const int *counts, int counts_len, int width) {
    long long current = 0;
    for (int i = 0; i < width && i < counts_len; i++) current += counts[i];
    long long best = current;
    for (int i = width; i < counts_len; i++) {
        current += (long long) counts[i] - counts[i - width];
        if (current > best) best = current;
    }
    return best;
}
