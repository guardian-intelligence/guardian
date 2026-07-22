import type {
  FullResult,
  Reporter,
  Suite,
  TestCase,
  TestError,
  TestResult,
} from "@playwright/test/reporter";
import { registryFromEnv, type RedactionRegistry } from "./redact.ts";

// Emits one scrubbed JSON line per event. Playwright's stock reporters print
// step call logs and error context that can carry typed values; this one
// serializes only fields we chose and runs every line through the redaction
// registry built from the credential env.
export default class RedactingReporter implements Reporter {
  private readonly registry: RedactionRegistry = registryFromEnv(process.env);

  private emit(event: Record<string, unknown>): void {
    process.stdout.write(`${this.registry.scrub(JSON.stringify(event))}\n`);
  }

  private formatError(error: TestError): string {
    return [error.message ?? "", error.stack ?? ""].filter(Boolean).join("\n");
  }

  onBegin(_config: unknown, suite: Suite): void {
    this.emit({ event: "begin", tests: suite.allTests().length });
  }

  onTestEnd(test: TestCase, result: TestResult): void {
    this.emit({
      event: "test",
      title: test.title,
      status: result.status,
      durationMs: result.duration,
      ...(result.error ? { error: this.formatError(result.error) } : {}),
    });
  }

  onError(error: TestError): void {
    this.emit({ event: "error", error: this.formatError(error) });
  }

  onEnd(result: FullResult): void {
    this.emit({ event: "end", status: result.status });
  }

  printsToStdio(): boolean {
    return true;
  }
}
