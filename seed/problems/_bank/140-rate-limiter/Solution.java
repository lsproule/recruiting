import java.util.*;

class Solution {
    public static int dropped_requests(long[] timestamps, int limit, long window) {
        ArrayDeque<Long> accepted = new ArrayDeque<>();
        int dropped = 0;
        for (long t : timestamps) {
            while (!accepted.isEmpty() && accepted.peekFirst() <= t - window) accepted.pollFirst();
            if (accepted.size() < limit) accepted.addLast(t); else dropped++;
        }
        return dropped;
    }
}
