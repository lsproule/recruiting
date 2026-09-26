def robot_position(commands: str) -> list[int]:
    x = y = 0
    dx, dy = 0, 1
    for c in commands:
        if c == "F":
            x += dx
            y += dy
        elif c == "L":
            dx, dy = -dy, dx
        elif c == "R":
            dx, dy = dy, -dx
    return [x, y]
