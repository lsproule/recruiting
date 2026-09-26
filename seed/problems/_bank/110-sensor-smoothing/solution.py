def moving_average(readings: list[float], width: int) -> list[float]:
    total = sum(readings[:width])
    out = [total / width]
    for i in range(width, len(readings)):
        total += readings[i] - readings[i - width]
        out.append(total / width)
    return out
