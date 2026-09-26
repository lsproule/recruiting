def peak_window(counts: list[int], width: int) -> int:
    current = sum(counts[:width])
    best = current
    for i in range(width, len(counts)):
        current += counts[i] - counts[i - width]
        best = max(best, current)
    return best
