using System;

class Solution {
    public static int min_max_load(int[] weights, int trucks) {
        int lo = 0, hi = 0;
        foreach (var w in weights) {
            lo = Math.Max(lo, w);
            hi += w;
        }
        while (lo < hi) {
            int mid = (lo + hi) / 2;
            if (Fits(weights, trucks, mid)) hi = mid; else lo = mid + 1;
        }
        return lo;
    }

    static bool Fits(int[] weights, int trucks, int capacity) {
        int used = 1, load = 0;
        foreach (var w in weights) {
            if (load + w > capacity) {
                used++;
                load = w;
            } else {
                load += w;
            }
        }
        return used <= trucks;
    }
}
