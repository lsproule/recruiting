#include <stdlib.h>
#include <string.h>

static int cmp_int(const void *a, const void *b) {
    int x = *(const int *) a, y = *(const int *) b;
    return (x > y) - (x < y);
}

int rooms_needed(const int *starts, int starts_len, const int *ends, int ends_len) {
    int *s = malloc(sizeof(int) * (size_t) (starts_len > 0 ? starts_len : 1));
    int *e = malloc(sizeof(int) * (size_t) (ends_len > 0 ? ends_len : 1));
    memcpy(s, starts, sizeof(int) * (size_t) starts_len);
    memcpy(e, ends, sizeof(int) * (size_t) ends_len);
    qsort(s, (size_t) starts_len, sizeof(int), cmp_int);
    qsort(e, (size_t) ends_len, sizeof(int), cmp_int);
    int rooms = 0, best = 0, j = 0;
    for (int i = 0; i < starts_len; i++) {
        while (j < ends_len && e[j] <= s[i]) {
            rooms--;
            j++;
        }
        rooms++;
        if (rooms > best) best = rooms;
    }
    free(s);
    free(e);
    return best;
}
