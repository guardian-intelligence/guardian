export class BoundedGate {
  private active = 0;

  constructor(readonly limit: number) {
    if (!Number.isSafeInteger(limit) || limit <= 0) {
      throw new TypeError("bounded gate limit must be a positive integer");
    }
  }

  tryEnter(): (() => void) | null {
    if (this.active >= this.limit) return null;
    this.active += 1;
    let released = false;
    return () => {
      if (released) return;
      released = true;
      this.active -= 1;
    };
  }
}
