import heapq


def deploy_order(services: list[str], deps: list[list[str]]) -> list[str]:
    indegree = {s: 0 for s in services}
    after: dict[str, list[str]] = {s: [] for s in services}
    for before, then in deps:
        after[before].append(then)
        indegree[then] += 1
    ready = [s for s in services if indegree[s] == 0]
    heapq.heapify(ready)
    order = []
    while ready:
        s = heapq.heappop(ready)
        order.append(s)
        for t in after[s]:
            indegree[t] -= 1
            if indegree[t] == 0:
                heapq.heappush(ready, t)
    return order if len(order) == len(services) else []
