#include <string>
#include <vector>

std::vector<int> robot_position(std::string commands) {
    int x = 0, y = 0, dx = 0, dy = 1;
    for (char c : commands) {
        if (c == 'F') {
            x += dx;
            y += dy;
        } else if (c == 'L') {
            int t = dx; dx = -dy; dy = t;
        } else if (c == 'R') {
            int t = dx; dx = dy; dy = -t;
        }
    }
    return {x, y};
}
