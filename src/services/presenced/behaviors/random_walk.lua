-- v1: every few ticks, each dog takes one step in a random direction.
-- rand(n) is host-provided and deterministic in (dog, tick), so identical
-- scripts in live and shadow slots produce zero divergence.
function step(tick, dog, x, y, w, h)
  if tick % 6 ~= 0 then
    return 0, 0
  end
  local r = rand(4)
  if r == 0 then return 1, 0 end
  if r == 1 then return -1, 0 end
  if r == 2 then return 0, 1 end
  return 0, -1
end
