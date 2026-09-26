#include <stdlib.h>

int min_coins(const int *coins, int coins_len, int amount) {
    int unreachable = amount + 1;
    int *best = malloc(sizeof(int) * (size_t) (amount + 1));
    best[0] = 0;
    for (int a = 1; a <= amount; a++) {
        best[a] = unreachable;
        for (int i = 0; i < coins_len; i++) {
            int c = coins[i];
            if (c <= a && best[a - c] + 1 < best[a]) best[a] = best[a - c] + 1;
        }
    }
    int answer = best[amount] == unreachable ? -1 : best[amount];
    free(best);
    return answer;
}
