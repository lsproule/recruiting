package main

func moving_average(readings []float64, width int) []float64 {
	total := 0.0
	for i := 0; i < width; i++ {
		total += readings[i]
	}
	out := []float64{total / float64(width)}
	for i := width; i < len(readings); i++ {
		total += readings[i] - readings[i-width]
		out = append(out, total/float64(width))
	}
	return out
}
