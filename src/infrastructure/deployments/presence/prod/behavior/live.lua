-- v3: orbit with a breathing ring - each dog circles the park center on
-- its id-derived ring, and the ring swells and contracts +-4 cells on a
-- ~10s cycle. Pure function of the inputs - a shadow copy diffs to zero
-- divergence by construction.
function step(tick, dog, x, y, w, h)
  if tick % 2 ~= 0 then
    return 0, 0
  end
  local rx, ry = x - w / 2, y - h / 2
  local r = math.sqrt(rx * rx + ry * ry)
  if r < 0.5 then return 1, 0 end
  local ring = 10 + ((string.byte(dog, 1) or 0) + (string.byte(dog, 2) or 0)) % 28
  ring = ring + 4 * math.sin(tick / 40)
  local g = (ring - r) * 0.25
  if g > 1 then g = 1 elseif g < -1 then g = -1 end
  local vx = -ry / r + g * rx / r
  local vy = rx / r + g * ry / r
  local sx = vx > 0.35 and 1 or (vx < -0.35 and -1 or 0)
  local sy = vy > 0.35 and 1 or (vy < -0.35 and -1 or 0)
  return sx, sy
end
