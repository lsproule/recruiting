/**
 * @param {number[]} readings
 * @param {number} width
 * @returns {number[]}
 */
function moving_average(readings, width) {
  let total = 0;
  for (let i = 0; i < width; i++) total += readings[i];
  const out = [total / width];
  for (let i = width; i < readings.length; i++) {
    total += readings[i] - readings[i - width];
    out.push(total / width);
  }
  return out;
}
