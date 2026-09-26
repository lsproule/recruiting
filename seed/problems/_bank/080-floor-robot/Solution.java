class Solution {
    public static int[] robot_position(String commands) {
        int x = 0, y = 0, dx = 0, dy = 1;
        for (int i = 0; i < commands.length(); i++) {
            char c = commands.charAt(i);
            if (c == 'F') {
                x += dx;
                y += dy;
            } else if (c == 'L') {
                int t = dx; dx = -dy; dy = t;
            } else if (c == 'R') {
                int t = dx; dx = dy; dy = -t;
            }
        }
        return new int[] {x, y};
    }
}
