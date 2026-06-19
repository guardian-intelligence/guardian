# @guardian-intelligence/aisucks

Tiny SDK for the aisucks.app public API. Release artifacts are built through
Guardian's provenance-preserving release lane.

```ts
import { AisucksClient } from "@guardian-intelligence/aisucks";

const client = new AisucksClient();
const health = await client.health();
console.log(health.capabilities);
```
