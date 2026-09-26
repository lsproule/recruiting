def robot_position(commands)
  x = y = 0
  dx, dy = 0, 1
  commands.each_char do |c|
    case c
    when "F"
      x += dx
      y += dy
    when "L"
      dx, dy = -dy, dx
    when "R"
      dx, dy = dy, -dx
    end
  end
  [x, y]
end
