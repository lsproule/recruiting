def min_max_load(weights: list[int], trucks: int) -> int:
    def fits(capacity: int) -> bool:
        used, load = 1, 0
        for w in weights:
            if load + w > capacity:
                used += 1
                load = w
            else:
                load += w
        return used <= trucks

    lo, hi = max(weights), sum(weights)
    while lo < hi:
        mid = (lo + hi) // 2
        if fits(mid):
            hi = mid
        else:
            lo = mid + 1
    return lo
