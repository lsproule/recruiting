#include <vector>

int min_coins(std::vector<int> coins, int amount) {
    int unreachable = amount + 1;
    std::vector<int> best(amount + 1, unreachable);
    best[0] = 0;
    for (int a = 1; a <= amount; a++) {
        for (int c : coins) {
            if (c <= a && best[a - c] + 1 < best[a]) best[a] = best[a - c] + 1;
        }
    }
    return best[amount] == unreachable ? -1 : best[amount];
}
