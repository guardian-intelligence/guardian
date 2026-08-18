import type {
  FullResult,
  Reporter,
  Suite,
  TestCase,
  TestError,
  TestResult,
} from "@playwright/test/reporter";
import { FINDING_ATTACHMENT } from "./findings.ts";

// This canary handles no credentials, but scrubbing query strings from any
// URL that reaches the log sink is cheap defense-in-depth against future
// targets that put tokens in links.
export function scrubUrlQueries(text: string): string {
  return text.replace(/(https?:\/\/[^\s"'`?#]+)\?[^\s"'`]*/g, "$1?[scrubbed]");
}

// Emits one scrubbed JSON line per event, mirroring the journey canaries'
// reporter contract so downstream log tooling parses both streams the same
// way.
export default class VisualReporter implements Reporter {
  private findings = 0;

  private emit(event: Record<string, unknown>): void {
    process.stdout.write(`${scrubUrlQueries(JSON.stringify(event))}\n`);
  }

  private formatError(error: TestError): string {
    return [error.message ?? "", error.stack ?? ""].filter(Boolean).join("\n");
  }

  onBegin(_config: unknown, suite: Suite): void {
    this.emit({ event: "begin", tests: suite.allTests().length });
  }

  // Worker stdout/stderr is captured by the runner and only reaches the pod
  // log if a reporter forwards it — without this, a hang is a bare "Test
  // timeout" with no location.
  onStdOut(chunk: string | Buffer): void {
    process.stdout.write(scrubUrlQueries(chunk.toString()));
  }

  onStdErr(chunk: string | Buffer): void {
    process.stdout.write(scrubUrlQueries(chunk.toString()));
  }

  onTestEnd(test: TestCase, result: TestResult): void {
    for (const attachment of result.attachments) {
      if (attachment.name !== FINDING_ATTACHMENT || !attachment.body) continue;
      this.findings += 1;
      this.emit({ event: "finding", ...JSON.parse(attachment.body.toString()) });
    }
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
    this.emit({ event: "end", status: result.status, findings: this.findings });
  }

  printsToStdio(): boolean {
    return true;
  }
}
