def receipt_total(quantities: list[int], prices: list[int]) -> int:
    return sum(q * p for q, p in zip(quantities, prices))
