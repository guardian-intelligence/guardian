import { Context, Effect } from "effect";
import type {
  CanonicalCheckoutTarget,
  CanonicalWorkspace,
  CheckoutPathInput,
  TempPack,
} from "../domain.ts";
import type { WorkspaceEscape, WorkspaceFailure } from "../errors.ts";

export interface PreparedWorkspace {
  readonly target: CanonicalCheckoutTarget;
  readonly workspace: CanonicalWorkspace;
}

export interface WorkspaceService {
  readonly createTempPack: Effect.Effect<TempPack, WorkspaceFailure>;
  readonly prepareTarget: (
    workspace: string,
    requestedPath: CheckoutPathInput,
  ) => Effect.Effect<PreparedWorkspace, WorkspaceEscape | WorkspaceFailure>;
  readonly removeTempPack: (tempPack: TempPack) => Effect.Effect<void>;
}

export class Workspace extends Context.Tag("@guardian/postflight-checkout/Workspace")<
  Workspace,
  WorkspaceService
>() {}
