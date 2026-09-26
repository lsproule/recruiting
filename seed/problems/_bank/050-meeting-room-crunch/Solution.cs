using System;

class Solution {
    public static int rooms_needed(int[] starts, int[] ends) {
        var s = (int[]) starts.Clone();
        var e = (int[]) ends.Clone();
        Array.Sort(s);
        Array.Sort(e);
        int rooms = 0, best = 0, j = 0;
        foreach (var start in s) {
            while (j < e.Length && e[j] <= start) {
                rooms--;
                j++;
            }
            rooms++;
            best = Math.Max(best, rooms);
        }
        return best;
    }
}
