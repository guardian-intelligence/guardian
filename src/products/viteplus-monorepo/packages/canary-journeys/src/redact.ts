// Canaries hold real credentials (docs/canaries.md): every string that leaves
// the process passes through a registry of known secret values. Registration
// happens where secrets are minted, so scrubbing never depends on guessing
// what a secret looks like.
export class RedactionRegistry {
  private readonly secrets = new Map<string, string>();

  register(name: string, value: string | undefined): void {
    const trimmed = value?.trim();
    if (!trimmed || trimmed.length < 4) {
      return;
    }
    this.secrets.set(trimmed, name);
  }

  // A base32 seed leaks through trivially derived forms.
  registerSeed(name: string, seed: string | undefined): void {
    if (!seed) {
      return;
    }
    const normalized = seed.trim().replaceAll(" ", "");
    this.register(name, seed);
    this.register(name, normalized);
    this.register(name, normalized.toUpperCase());
    this.register(name, normalized.toLowerCase());
  }

  scrub(text: string): string {
    let out = text;
    for (const [value, name] of this.secrets) {
      out = out.split(value).join(`[REDACTED:${name}]`);
    }
    return out;
  }
}

export function registryFromEnv(env: Record<string, string | undefined>): RedactionRegistry {
  const registry = new RedactionRegistry();
  registry.register("github-password", env.GITHUB_CANARY_PASSWORD);
  registry.registerSeed("github-totp-seed", env.GITHUB_CANARY_TOTP_SECRET);
  return registry;
}
