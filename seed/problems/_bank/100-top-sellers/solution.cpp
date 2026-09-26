#include <algorithm>
#include <map>
#include <string>
#include <vector>

std::vector<std::string> top_sellers(std::map<std::string, int> sales, int k) {
    std::vector<std::string> names;
    for (const auto& entry : sales) names.push_back(entry.first);
    std::sort(names.begin(), names.end(), [&](const std::string& a, const std::string& b) {
        if (sales[a] != sales[b]) return sales[a] > sales[b];
        return a < b;
    });
    if (k < static_cast<int>(names.size())) names.resize(k);
    return names;
}
