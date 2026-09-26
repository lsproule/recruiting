from collections import deque


def dropped_requests(timestamps: list[int], limit: int, window: int) -> int:
    accepted: deque[int] = deque()
    dropped = 0
    for t in timestamps:
        while accepted and accepted[0] <= t - window:
            accepted.popleft()
        if len(accepted) < limit:
            accepted.append(t)
        else:
            dropped += 1
    return dropped
