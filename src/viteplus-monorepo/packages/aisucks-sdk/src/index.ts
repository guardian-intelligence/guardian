export interface AisucksClientOptions {
  baseUrl?: string;
  fetch?: typeof globalThis.fetch;
}

export interface HelloResponse {
  message: string;
  service: "aisucks";
  version: string;
}

export class AisucksError extends Error {
  readonly status: number;

  constructor(message: string, status: number) {
    super(message);
    this.name = "AisucksError";
    this.status = status;
  }
}

export class AisucksClient {
  readonly baseUrl: URL;
  readonly fetch: typeof globalThis.fetch;

  constructor(options: AisucksClientOptions = {}) {
    this.baseUrl = new URL(options.baseUrl ?? "https://aisucks.app");
    this.fetch = options.fetch ?? globalThis.fetch;
    if (typeof this.fetch !== "function") {
      throw new TypeError("AisucksClient requires a fetch implementation");
    }
  }

  async hello(): Promise<HelloResponse> {
    return this.getJSON<HelloResponse>("/api/v1/hello");
  }

  private async getJSON<T>(path: string): Promise<T> {
    const url = new URL(path, this.baseUrl);
    const response = await this.fetch(url, {
      method: "GET",
      headers: { Accept: "application/json" },
    });
    if (!response.ok) {
      throw new AisucksError(`aisucks request failed with HTTP ${response.status}`, response.status);
    }
    return (await response.json()) as T;
  }
}

export function hello(options?: AisucksClientOptions): Promise<HelloResponse> {
  return new AisucksClient(options).hello();
}
