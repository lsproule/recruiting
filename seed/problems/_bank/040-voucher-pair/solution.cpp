#include <unordered_map>
#include <vector>

std::vector<int> voucher_pair(std::vector<int> amounts, int target) {
    std::unordered_map<int, int> seen;
    for (int j = 0; j < static_cast<int>(amounts.size()); j++) {
        auto it = seen.find(target - amounts[j]);
        if (it != seen.end()) return {it->second, j};
        seen.emplace(amounts[j], j);
    }
    return {};
}
