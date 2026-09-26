#include <stdlib.h>

double *moving_average(const double *readings, int readings_len, int width, int *out_len) {
    int n = readings_len - width + 1;
    if (n < 0) n = 0;
    double *out = malloc(sizeof(double) * (size_t) (n > 0 ? n : 1));
    double total = 0;
    for (int i = 0; i < width && i < readings_len; i++) total += readings[i];
    if (n > 0) out[0] = total / width;
    for (int i = width; i < readings_len; i++) {
        total += readings[i] - readings[i - width];
        out[i - width + 1] = total / width;
    }
    *out_len = n;
    return out;
}
