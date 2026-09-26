import java.util.*;

class Solution {
    public static int[] voucher_pair(int[] amounts, int target) {
        Map<Integer, Integer> seen = new HashMap<>();
        for (int j = 0; j < amounts.length; j++) {
            Integer i = seen.get(target - amounts[j]);
            if (i != null) return new int[] {i, j};
            seen.putIfAbsent(amounts[j], j);
        }
        return new int[0];
    }
}
