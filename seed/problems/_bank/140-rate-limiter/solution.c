#include <stdlib.h>

int dropped_requests(const long long *timestamps, int timestamps_len, int limit, long long window) {
    long long *accepted = malloc(sizeof(long long) * (size_t) (timestamps_len > 0 ? timestamps_len : 1));
    int head = 0, tail = 0, dropped = 0;
    for (int i = 0; i < timestamps_len; i++) {
        long long t = timestamps[i];
        while (head < tail && accepted[head] <= t - window) head++;
        if (tail - head < limit) accepted[tail++] = t; else dropped++;
    }
    free(accepted);
    return dropped;
}
