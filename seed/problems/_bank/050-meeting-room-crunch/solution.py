def rooms_needed(starts: list[int], ends: list[int]) -> int:
    starts = sorted(starts)
    ends = sorted(ends)
    rooms = best = 0
    j = 0
    for s in starts:
        while j < len(ends) and ends[j] <= s:
            rooms -= 1
            j += 1
        rooms += 1
        best = max(best, rooms)
    return best
