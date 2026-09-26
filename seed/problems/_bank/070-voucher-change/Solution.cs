class Solution {
    public static int min_coins(int[] coins, int amount) {
        int unreachable = amount + 1;
        var best = new int[amount + 1];
        for (int a = 1; a <= amount; a++) {
            best[a] = unreachable;
            foreach (var c in coins) {
                if (c <= a && best[a - c] + 1 < best[a]) best[a] = best[a - c] + 1;
            }
        }
        return best[amount] == unreachable ? -1 : best[amount];
    }
}
