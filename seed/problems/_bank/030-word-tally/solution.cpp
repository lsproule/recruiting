#include <map>
#include <string>
#include <vector>

std::map<std::string, int> word_tally(std::vector<std::string> words) {
    std::map<std::string, int> counts;
    for (const auto& w : words) counts[w]++;
    return counts;
}
