import { Context, Effect } from "effect";
import type { CheckoutBundleRequest, PackMetadata } from "../domain.ts";
import type { HostError } from "../errors.ts";

export interface CheckoutHostService {
  readonly acquirePack: (request: CheckoutBundleRequest) => Effect.Effect<PackMetadata, HostError>;
}

export class CheckoutHost extends Context.Tag("@guardian/postflight-checkout/CheckoutHost")<
  CheckoutHost,
  CheckoutHostService
>() {}
