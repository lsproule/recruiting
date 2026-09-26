class Solution {
    public static int[] robot_position(string commands) {
        int x = 0, y = 0, dx = 0, dy = 1;
        foreach (var c in commands) {
            if (c == 'F') {
                x += dx;
                y += dy;
            } else if (c == 'L') {
                (dx, dy) = (-dy, dx);
            } else if (c == 'R') {
                (dx, dy) = (dy, -dx);
            }
        }
        return new[] { x, y };
    }
}
