#include <algorithm>
#include <vector>

long long peak_window(std::vector<int> counts, int width) {
    long long current = 0;
    for (int i = 0; i < width; i++) current += counts[i];
    long long best = current;
    for (size_t i = width; i < counts.size(); i++) {
        current += static_cast<long long>(counts[i]) - counts[i - width];
        best = std::max(best, current);
    }
    return best;
}
