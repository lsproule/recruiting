/**
 * @param {string} commands
 * @returns {number[]}
 */
function robot_position(commands) {
  let x = 0, y = 0, dx = 0, dy = 1;
  for (const c of commands) {
    if (c === "F") {
      x += dx;
      y += dy;
    } else if (c === "L") {
      [dx, dy] = [-dy, dx];
    } else if (c === "R") {
      [dx, dy] = [dy, -dx];
    }
  }
  return [x, y];
}
