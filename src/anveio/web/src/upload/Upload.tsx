import { useCallback, useEffect, useRef, useState } from "react";
import * as v from "valibot";
import {
  api,
  LinkError,
  type Progress,
  read,
  RECEIPTS,
  type Receipt,
  Receipts,
  upload,
  write,
} from "./engine";
import { STRINGS, type StringKey } from "./strings";
import { STREAMS, type Stream } from "./streams";

// anveio.com/upload#<link>: the supplier's farmhands open the shared link on
// their phones, pick videos from the gallery and leave the page open. The
// link's secret lives in the fragment (never sent in a request line or a
// Referer) and is remembered on the phone, so a home-screen shortcut or a
// bare /upload keeps working after the first visit.

const LINK = "anveio.upload.link";
const NAME = "anveio.upload.name";
const STREAM = "anveio.upload.stream";

const bn = new Intl.NumberFormat("bn-BD", { maximumFractionDigits: 1 });

type LinkState = "checking" | "ok" | "bad";

interface Item {
  readonly id: number;
  readonly file: File;
  readonly who: { readonly name: string; readonly stream: Stream };
  progress: Progress;
}

function T({ k }: { k: StringKey }) {
  const [bengali, english] = STRINGS[k];
  return (
    <>
      <span lang="bn">{bengali}</span>
      <span lang="en" className="en">
        {english}
      </span>
    </>
  );
}

const size = (bytes: number) =>
  bytes >= 1024 ** 3 ? `${bn.format(bytes / 1024 ** 3)} GB` : `${bn.format(bytes / 1024 ** 2)} MB`;
const minutes = (seconds: number) => `${bn.format(seconds / 60)} মিনিট`;

export function Upload() {
  const [token, setToken] = useState<string>();
  const [link, setLink] = useState<LinkState>("checking");
  const [name, setName] = useState("");
  const [stream, setStream] = useState<Stream>("drone");
  const [items, setItems] = useState<Item[]>([]);
  const [receipts, setReceipts] = useState<Receipt[]>([]);
  const [nameMissing, setNameMissing] = useState(false);
  const queue = useRef<Item[]>([]);
  const running = useRef(false);
  const nextId = useRef(0);
  const input = useRef<HTMLInputElement>(null);

  useEffect(() => {
    const fromHash = window.location.hash.slice(1);
    const stored = read(LINK, v.string(), "");
    const found = /^[A-Za-z0-9_-]{32,128}$/.test(fromHash) ? fromHash : stored;
    if (found !== stored) write(LINK, found);
    setName(read(NAME, v.string(), ""));
    setStream(read(STREAM, v.picklist(STREAMS), "drone"));
    setReceipts(read(RECEIPTS, Receipts, []));
    if (!found) return setLink("bad");
    setToken(found);
    api(found)("hello", {}, v.object({}))
      .then(() => setLink("ok"))
      .catch((error) => setLink(error instanceof LinkError ? "bad" : "ok"));
  }, []);

  const busy = items.some((i) => !["done", "failed"].includes(i.progress.phase));

  // Keep the screen on while anything is sending, and warn before leaving.
  useEffect(() => {
    if (!busy) return;
    let lock: WakeLockSentinel | undefined;
    const acquire = () => {
      if (document.visibilityState === "visible") {
        navigator.wakeLock?.request("screen").then(
          (l) => (lock = l),
          () => {},
        );
      }
    };
    const leave = (e: BeforeUnloadEvent) => e.preventDefault();
    acquire();
    document.addEventListener("visibilitychange", acquire);
    window.addEventListener("beforeunload", leave);
    return () => {
      document.removeEventListener("visibilitychange", acquire);
      window.removeEventListener("beforeunload", leave);
      void lock?.release();
    };
  }, [busy]);

  const refresh = () => setItems([...queue.current]);

  // One video at a time, in the order picked; each keeps the name and kind
  // that were set when it was picked.
  const pump = useCallback(async (call: ReturnType<typeof api>) => {
    if (running.current) return;
    running.current = true;
    const next = () => queue.current.find((i) => i.progress.phase === "waiting");
    for (let item = next(); item; item = next()) {
      const current = item;
      try {
        await upload(current.file, current.who, call, (p) => {
          current.progress = p;
          refresh();
        });
        setReceipts(read(RECEIPTS, Receipts, []));
      } catch (error) {
        current.progress = { ...current.progress, phase: "failed" };
        refresh();
        if (error instanceof LinkError) setLink("bad");
      }
    }
    running.current = false;
  }, []);

  const choose = () => {
    if (!name.trim()) return setNameMissing(true);
    input.current?.click();
  };

  const picked = (files: FileList | null) => {
    if (!files || !token) return;
    for (const file of Array.from(files)) {
      const progress: Progress = { phase: "waiting", sent: 0, total: file.size };
      queue.current.push({
        id: nextId.current++,
        file,
        who: { name: name.trim(), stream },
        progress,
      });
    }
    refresh();
    if (input.current) input.current.value = "";
    void pump(api(token));
  };

  const totalSeconds = receipts.reduce((sum, r) => sum + (r.seconds ?? 0), 0);

  if (link === "bad") {
    return (
      <main className="upload">
        <h1>
          <T k="title" />
        </h1>
        <p className="notice bad">
          <T k="badLink" />
        </p>
      </main>
    );
  }

  return (
    <main className="upload">
      <h1>
        <T k="title" />
      </h1>
      <p className="intro">
        <T k="intro" />
      </p>

      <label className="field">
        <span className="label">
          <T k="name" />
        </span>
        <input
          type="text"
          autoComplete="name"
          value={name}
          aria-invalid={nameMissing && !name.trim()}
          onChange={(e) => {
            setName(e.target.value);
            setNameMissing(false);
            write(NAME, e.target.value.trim());
          }}
        />
        {nameMissing && !name.trim() ? (
          <span className="error">
            <T k="needName" />
          </span>
        ) : null}
      </label>

      <fieldset className="field">
        <legend className="label">
          <T k="stream" />
        </legend>
        <div className="streams">
          {STREAMS.map((s) => (
            <label key={s} className="stream">
              <input
                type="radio"
                name="stream"
                value={s}
                checked={stream === s}
                onChange={() => {
                  setStream(s);
                  write(STREAM, s);
                }}
              />
              <span>
                <T k={s} />
              </span>
            </label>
          ))}
        </div>
      </fieldset>

      <button type="button" className="choose" disabled={link !== "ok"} onClick={choose}>
        {link === "ok" ? <T k="choose" /> : <T k="checking" />}
      </button>
      <input
        ref={input}
        type="file"
        accept="video/*"
        multiple
        hidden
        onChange={(e) => picked(e.target.files)}
      />

      {items.length > 0 ? (
        <section className="queue">
          {busy ? (
            <p className="notice">
              <T k="keepOpen" />
            </p>
          ) : null}
          <ul>
            {items.map(({ id, file, progress }) => (
              <li key={id} className={`item ${progress.phase}`}>
                <div className="row">
                  <span className="file">{file.name}</span>
                  <span className="size">{size(file.size)}</span>
                </div>
                <progress max={progress.total || 1} value={progress.sent} />
                <div className="row status">
                  <span>
                    <T k={progress.phase} />
                  </span>
                  <span>
                    {progress.phase === "done" && progress.seconds
                      ? minutes(progress.seconds)
                      : `${bn.format(Math.floor((100 * progress.sent) / (progress.total || 1)))}%`}
                  </span>
                </div>
              </li>
            ))}
          </ul>
          <p className="hint">
            <T k="resume" />
          </p>
        </section>
      ) : null}

      {receipts.length > 0 ? (
        <section className="receipts">
          <h2>
            <T k="sent" />
          </h2>
          <p className="total">
            {bn.format(receipts.length)}টি ভিডিও · {minutes(totalSeconds)}
            <span lang="en" className="en">
              {receipts.length} videos · {Math.round((totalSeconds / 60) * 10) / 10} min
            </span>
          </p>
          <ul>
            {receipts.slice(0, 50).map((r) => (
              <li key={`${r.at}:${r.filename}`} className="row">
                <span className="file">{r.filename}</span>
                <span className="size">{r.seconds ? minutes(r.seconds) : size(r.size)}</span>
              </li>
            ))}
          </ul>
        </section>
      ) : null}
    </main>
  );
}
