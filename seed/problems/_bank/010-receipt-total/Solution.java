class Solution {
    public static long receipt_total(int[] quantities, int[] prices) {
        long total = 0;
        for (int i = 0; i < quantities.length; i++) {
            total += (long) quantities[i] * prices[i];
        }
        return total;
    }
}
