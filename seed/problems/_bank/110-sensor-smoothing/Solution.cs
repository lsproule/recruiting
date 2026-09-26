class Solution {
    public static double[] moving_average(double[] readings, int width) {
        double total = 0;
        for (int i = 0; i < width; i++) total += readings[i];
        var result = new double[readings.Length - width + 1];
        result[0] = total / width;
        for (int i = width; i < readings.Length; i++) {
            total += readings[i] - readings[i - width];
            result[i - width + 1] = total / width;
        }
        return result;
    }
}
