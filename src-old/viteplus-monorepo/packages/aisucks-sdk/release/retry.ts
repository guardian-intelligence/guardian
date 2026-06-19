import { Duration, Effect } from "effect";

export function retryTransient<A, E, R>(
  operation: Effect.Effect<A, E, R>,
  isTransient: (error: E) => boolean,
  attempts = 3,
  delayMs = 500,
): Effect.Effect<A, E, R> {
  const loop = (remaining: number): Effect.Effect<A, E, R> =>
    operation.pipe(
      Effect.catchAll((error) => {
        if (remaining <= 0 || !isTransient(error)) {
          return Effect.fail(error);
        }
        return Effect.sleep(Duration.millis(delayMs)).pipe(
          Effect.flatMap(() => loop(remaining - 1)),
        );
      }),
    );

  return loop(attempts - 1);
}
