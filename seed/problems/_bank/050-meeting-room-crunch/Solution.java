import java.util.*;

class Solution {
    public static int rooms_needed(int[] starts, int[] ends) {
        int[] s = starts.clone();
        int[] e = ends.clone();
        Arrays.sort(s);
        Arrays.sort(e);
        int rooms = 0, best = 0, j = 0;
        for (int start : s) {
            while (j < e.length && e[j] <= start) {
                rooms--;
                j++;
            }
            rooms++;
            best = Math.max(best, rooms);
        }
        return best;
    }
}
