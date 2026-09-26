#include <stdlib.h>

int *robot_position(const char *commands, int *out_len) {
    int x = 0, y = 0, dx = 0, dy = 1;
    for (const char *p = commands; *p; p++) {
        if (*p == 'F') {
            x += dx;
            y += dy;
        } else if (*p == 'L') {
            int t = dx; dx = -dy; dy = t;
        } else if (*p == 'R') {
            int t = dx; dx = dy; dy = -t;
        }
    }
    int *res = malloc(sizeof(int) * 2);
    res[0] = x;
    res[1] = y;
    *out_len = 2;
    return res;
}
