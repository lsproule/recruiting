class Solution {
    public static double[] moving_average(double[] readings, int width) {
        double total = 0;
        for (int i = 0; i < width; i++) total += readings[i];
        double[] out = new double[readings.length - width + 1];
        out[0] = total / width;
        for (int i = width; i < readings.length; i++) {
            total += readings[i] - readings[i - width];
            out[i - width + 1] = total / width;
        }
        return out;
    }
}
