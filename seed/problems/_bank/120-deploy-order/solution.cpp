#include <functional>
#include <queue>
#include <string>
#include <unordered_map>
#include <vector>

std::vector<std::string> deploy_order(std::vector<std::string> services, std::vector<std::vector<std::string>> deps) {
    std::unordered_map<std::string, int> indegree;
    std::unordered_map<std::string, std::vector<std::string>> after;
    for (const auto& s : services) {
        indegree[s] = 0;
        after[s];
    }
    for (const auto& d : deps) {
        after[d[0]].push_back(d[1]);
        indegree[d[1]]++;
    }
    std::priority_queue<std::string, std::vector<std::string>, std::greater<std::string>> ready;
    for (const auto& s : services) if (indegree[s] == 0) ready.push(s);
    std::vector<std::string> order;
    while (!ready.empty()) {
        std::string s = ready.top();
        ready.pop();
        order.push_back(s);
        for (const auto& t : after[s]) {
            if (--indegree[t] == 0) ready.push(t);
        }
    }
    if (order.size() != services.size()) return {};
    return order;
}
