def voucher_pair(amounts: list[int], target: int) -> list[int]:
    seen: dict[int, int] = {}
    for j, a in enumerate(amounts):
        if target - a in seen:
            return [seen[target - a], j]
        seen.setdefault(a, j)
    return []
