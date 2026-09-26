using System.Collections.Generic;

class Solution {
    public static Dictionary<string, int> word_tally(string[] words) {
        var counts = new Dictionary<string, int>();
        foreach (var w in words) {
            counts[w] = counts.TryGetValue(w, out var n) ? n + 1 : 1;
        }
        return counts;
    }
}
