import { useCallback, useRef, useState } from "react";

export interface DropzoneProps {
  readonly onFile: (file: File) => void;
  readonly onWarm: () => void;
  readonly disabled: boolean;
}

// The hero IS the product: one glass card that accepts a video file. onWarm
// fires on first pointer-over so the engine chunk is fetched before the drop.
export function Dropzone({ onFile, onWarm, disabled }: DropzoneProps) {
  const inputRef = useRef<HTMLInputElement>(null);
  const [dragging, setDragging] = useState(false);

  const accept = useCallback(
    (files: FileList | null) => {
      const file = files?.[0];
      if (file) onFile(file);
    },
    [onFile],
  );

  return (
    <button
      type="button"
      disabled={disabled}
      onPointerEnter={onWarm}
      onFocus={onWarm}
      onClick={() => inputRef.current?.click()}
      onDragOver={(e) => {
        e.preventDefault();
        setDragging(true);
      }}
      onDragLeave={() => setDragging(false)}
      onDrop={(e) => {
        e.preventDefault();
        setDragging(false);
        accept(e.dataTransfer.files);
      }}
      className={`glass block w-full cursor-pointer px-10 py-16 text-center transition-all duration-200 hover:border-line-strong hover:bg-white/[0.05] ${
        dragging ? "border-glow-violet/60 bg-white/[0.06] scale-[1.01]" : ""
      } ${disabled ? "pointer-events-none opacity-60" : ""}`}
    >
      <input
        ref={inputRef}
        type="file"
        accept="video/mp4,video/quicktime,video/webm,video/*"
        className="hidden"
        onChange={(e) => accept(e.currentTarget.files)}
      />
      <div className="mx-auto mb-5 flex h-14 w-14 items-center justify-center rounded-2xl border border-line-strong bg-white/[0.04]">
        <svg
          viewBox="0 0 24 24"
          className="h-6 w-6 text-mist-dim"
          fill="none"
          stroke="currentColor"
          strokeWidth="1.5"
        >
          <path strokeLinecap="round" strokeLinejoin="round" d="M12 16V4m0 0L7 9m5-5 5 5M4 20h16" />
        </svg>
      </div>
      <p className="text-lg font-medium text-mist">Drop a video</p>
      <p className="mt-2 text-sm text-mist-faint">
        MP4, MOV, or WebM — it never leaves your device
      </p>
    </button>
  );
}
