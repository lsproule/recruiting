package main

func robot_position(commands string) []int {
	x, y, dx, dy := 0, 0, 0, 1
	for i := 0; i < len(commands); i++ {
		switch commands[i] {
		case 'F':
			x += dx
			y += dy
		case 'L':
			dx, dy = -dy, dx
		case 'R':
			dx, dy = dy, -dx
		}
	}
	return []int{x, y}
}
