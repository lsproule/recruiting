#include <vector>

std::vector<double> moving_average(std::vector<double> readings, int width) {
    double total = 0;
    for (int i = 0; i < width; i++) total += readings[i];
    std::vector<double> out;
    out.push_back(total / width);
    for (size_t i = width; i < readings.size(); i++) {
        total += readings[i] - readings[i - width];
        out.push_back(total / width);
    }
    return out;
}
