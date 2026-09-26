#include <stdlib.h>
#include <string.h>

/* Names are resolved to indices once by binary search over a sorted copy,
   then Kahn's algorithm picks the smallest ready service each round by a
   linear scan: with two thousand services that is a few million string
   compares at most. */
typedef struct { const char *name; int index; } named;

static int by_name(const void *a, const void *b) {
    return strcmp(((const named *) a)->name, ((const named *) b)->name);
}

static int lookup(const named *sorted, int n, const char *name) {
    int lo = 0, hi = n - 1;
    while (lo <= hi) {
        int mid = (lo + hi) / 2;
        int c = strcmp(sorted[mid].name, name);
        if (c == 0) return sorted[mid].index;
        if (c < 0) lo = mid + 1; else hi = mid - 1;
    }
    return -1;
}

static char *copy_name(const char *s) {
    size_t n = strlen(s);
    char *out = malloc(n + 1);
    memcpy(out, s, n + 1);
    return out;
}

char **deploy_order(const char **services, int services_len, const char ***deps, int deps_len, const int *deps_lens, int *out_len) {
    int n = services_len;
    named *sorted = malloc(sizeof(named) * (size_t) (n > 0 ? n : 1));
    for (int i = 0; i < n; i++) { sorted[i].name = services[i]; sorted[i].index = i; }
    qsort(sorted, (size_t) n, sizeof(named), by_name);

    int *indegree = calloc((size_t) (n > 0 ? n : 1), sizeof(int));
    int *head = malloc(sizeof(int) * (size_t) (n > 0 ? n : 1));
    int *next = malloc(sizeof(int) * (size_t) (deps_len > 0 ? deps_len : 1));
    int *to = malloc(sizeof(int) * (size_t) (deps_len > 0 ? deps_len : 1));
    for (int i = 0; i < n; i++) head[i] = -1;
    for (int i = 0; i < deps_len; i++) {
        if (deps_lens[i] < 2) continue;
        int a = lookup(sorted, n, deps[i][0]), b = lookup(sorted, n, deps[i][1]);
        to[i] = b;
        next[i] = head[a];
        head[a] = i;
        indegree[b]++;
    }

    char **order = malloc(sizeof(char *) * (size_t) (n > 0 ? n : 1));
    int done = 0;
    char *taken = calloc((size_t) (n > 0 ? n : 1), 1);
    for (;;) {
        /* the smallest name among the services with nothing left to wait for */
        int pick = -1;
        for (int k = 0; k < n; k++) {
            int i = sorted[k].index;
            if (!taken[i] && indegree[i] == 0) { pick = i; break; }
        }
        if (pick < 0) break;
        taken[pick] = 1;
        order[done++] = copy_name(services[pick]);
        for (int e = head[pick]; e != -1; e = next[e]) indegree[to[e]]--;
    }
    free(sorted); free(indegree); free(head); free(next); free(to); free(taken);
    if (done != n) {
        for (int i = 0; i < done; i++) free(order[i]);
        *out_len = 0;
        return order;
    }
    *out_len = done;
    return order;
}
