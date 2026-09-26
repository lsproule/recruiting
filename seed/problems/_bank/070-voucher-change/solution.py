def min_coins(coins: list[int], amount: int) -> int:
    unreachable = amount + 1
    best = [0] + [unreachable] * amount
    for a in range(1, amount + 1):
        for c in coins:
            if c <= a and best[a - c] + 1 < best[a]:
                best[a] = best[a - c] + 1
    return -1 if best[amount] == unreachable else best[amount]
