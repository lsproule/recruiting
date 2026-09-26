#include <vector>

long long receipt_total(std::vector<int> quantities, std::vector<int> prices) {
    long long total = 0;
    for (size_t i = 0; i < quantities.size(); i++) {
        total += static_cast<long long>(quantities[i]) * prices[i];
    }
    return total;
}
