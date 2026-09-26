fn robot_position(commands: String) -> Vec<i32> {
    let (mut x, mut y, mut dx, mut dy) = (0, 0, 0, 1);
    for c in commands.chars() {
        match c {
            'F' => { x += dx; y += dy; }
            'L' => { let t = dx; dx = -dy; dy = t; }
            'R' => { let t = dx; dx = dy; dy = -t; }
            _ => {}
        }
    }
    vec![x, y]
}
