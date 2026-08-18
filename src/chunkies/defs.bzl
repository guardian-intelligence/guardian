"""Visibility groups naming the chunkies surface a game is allowed to reach.

A game depends on the framework; the framework never depends on a game. This
list is the framework's game-facing API, so widening it is a deliberate
decision about what chunkies promises, not an incidental edit buried in a
BUILD file.
"""

GAME_FACING = [
    "//src/chunkies:__subpackages__",
    "//src/games:__subpackages__",
]
