using System.Collections.Generic;

class Solution {
    public static int dropped_requests(long[] timestamps, int limit, long window) {
        var accepted = new Queue<long>();
        int dropped = 0;
        foreach (var t in timestamps) {
            while (accepted.Count > 0 && accepted.Peek() <= t - window) accepted.Dequeue();
            if (accepted.Count < limit) accepted.Enqueue(t); else dropped++;
        }
        return dropped;
    }
}
