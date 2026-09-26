#include <stdlib.h>
#include <string.h>

typedef struct { const char *name; int units; } product;

static char *copy_name(const char *s) {
    size_t n = strlen(s);
    char *out = malloc(n + 1);
    memcpy(out, s, n + 1);
    return out;
}

static int by_rank(const void *a, const void *b) {
    const product *x = a, *y = b;
    if (x->units != y->units) return y->units - x->units;
    return strcmp(x->name, y->name);
}

char **top_sellers(const char **sales_keys, const int *sales_values, int sales_len, int k, int *out_len) {
    product *all = malloc(sizeof(product) * (size_t) (sales_len > 0 ? sales_len : 1));
    for (int i = 0; i < sales_len; i++) {
        all[i].name = sales_keys[i];
        all[i].units = sales_values[i];
    }
    qsort(all, (size_t) sales_len, sizeof(product), by_rank);
    int n = k < sales_len ? k : sales_len;
    if (n < 0) n = 0;
    char **res = malloc(sizeof(char *) * (size_t) (n > 0 ? n : 1));
    for (int i = 0; i < n; i++) res[i] = copy_name(all[i].name);
    free(all);
    *out_len = n;
    return res;
}
