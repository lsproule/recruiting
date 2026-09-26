#include <stdlib.h>

/* Sort positions by value, then walk from both ends. */
static const int *amounts_ref;
static int by_value(const void *a, const void *b) {
    int x = amounts_ref[*(const int *) a], y = amounts_ref[*(const int *) b];
    return (x > y) - (x < y);
}

int *voucher_pair(const int *amounts, int amounts_len, int target, int *out_len) {
    int *order = malloc(sizeof(int) * (size_t) amounts_len);
    for (int i = 0; i < amounts_len; i++) order[i] = i;
    amounts_ref = amounts;
    qsort(order, (size_t) amounts_len, sizeof(int), by_value);
    int *res = malloc(sizeof(int) * 2);
    *out_len = 0;
    int lo = 0, hi = amounts_len - 1;
    while (lo < hi) {
        long long sum = (long long) amounts[order[lo]] + amounts[order[hi]];
        if (sum == target) {
            int a = order[lo], b = order[hi];
            res[0] = a < b ? a : b;
            res[1] = a < b ? b : a;
            *out_len = 2;
            break;
        }
        if (sum < target) lo++; else hi--;
    }
    free(order);
    return res;
}
