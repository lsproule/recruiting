import java.util.*;

class Solution {
    public static int min_coins(int[] coins, int amount) {
        int unreachable = amount + 1;
        int[] best = new int[amount + 1];
        Arrays.fill(best, unreachable);
        best[0] = 0;
        for (int a = 1; a <= amount; a++) {
            for (int c : coins) {
                if (c <= a && best[a - c] + 1 < best[a]) best[a] = best[a - c] + 1;
            }
        }
        return best[amount] == unreachable ? -1 : best[amount];
    }
}
