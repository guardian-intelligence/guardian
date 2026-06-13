import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";

import { AisucksService, type HealthResponse } from "./gen/guardian/products/aisucks/v1/aisucks_pb.js";

export type { HealthResponse };

export interface AisucksClientOptions {
  baseUrl?: string;
  fetch?: typeof globalThis.fetch;
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

  async health(): Promise<HealthResponse> {
    const transport = createConnectTransport({
      baseUrl: this.baseUrl.toString(),
      fetch: this.fetch,
    });
    const client = createClient(AisucksService, transport);
    return client.health({});
  }
}

export function health(options?: AisucksClientOptions): Promise<HealthResponse> {
  return new AisucksClient(options).health();
}
