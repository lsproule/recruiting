#include <algorithm>
#include <vector>

int rooms_needed(std::vector<int> starts, std::vector<int> ends) {
    std::sort(starts.begin(), starts.end());
    std::sort(ends.begin(), ends.end());
    int rooms = 0, best = 0;
    size_t j = 0;
    for (int start : starts) {
        while (j < ends.size() && ends[j] <= start) {
            rooms--;
            j++;
        }
        rooms++;
        best = std::max(best, rooms);
    }
    return best;
}
