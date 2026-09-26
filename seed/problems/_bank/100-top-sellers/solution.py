def top_sellers(sales: dict[str, int], k: int) -> list[str]:
    ranked = sorted(sales, key=lambda name: (-sales[name], name))
    return ranked[:k]
