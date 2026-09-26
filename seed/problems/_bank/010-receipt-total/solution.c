long long receipt_total(const int *quantities, int quantities_len, const int *prices, int prices_len) {
    long long total = 0;
    for (int i = 0; i < quantities_len && i < prices_len; i++) {
        total += (long long) quantities[i] * prices[i];
    }
    return total;
}
