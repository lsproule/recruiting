using System.Collections.Generic;

class Solution {
    public static int[] voucher_pair(int[] amounts, int target) {
        var seen = new Dictionary<int, int>();
        for (int j = 0; j < amounts.Length; j++) {
            if (seen.TryGetValue(target - amounts[j], out var i)) return new[] { i, j };
            if (!seen.ContainsKey(amounts[j])) seen[amounts[j]] = j;
        }
        return new int[0];
    }
}
