class Solution {
    public static long receipt_total(int[] quantities, int[] prices) {
        long total = 0;
        for (int i = 0; i < quantities.Length; i++) {
            total += (long) quantities[i] * prices[i];
        }
        return total;
    }
}
