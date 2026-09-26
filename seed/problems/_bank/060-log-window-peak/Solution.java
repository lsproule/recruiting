class Solution {
    public static long peak_window(int[] counts, int width) {
        long current = 0;
        for (int i = 0; i < width; i++) current += counts[i];
        long best = current;
        for (int i = width; i < counts.length; i++) {
            current += (long) counts[i] - counts[i - width];
            best = Math.max(best, current);
        }
        return best;
    }
}
