import { useCallback, useRef, useState } from "react";

export interface DropzoneProps {
  readonly onFile: (file: File) => void;
  readonly onWarm: () => void;
  readonly disabled: boolean;
}

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
    <>
      <input
        ref={inputRef}
        type="file"
        accept="video/mp4,video/quicktime,video/webm,video/*"
        className="hidden"
        onChange={(e) => accept(e.currentTarget.files)}
      />
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
        className={`dropzone ${dragging ? "dropzone--dragging" : ""} ${
          disabled ? "dropzone--disabled" : ""
        }`}
      >
        <span className="dropzone__light" aria-hidden="true" />
        <span className="dropzone__corners" aria-hidden="true" />
        <span className="dropzone__content">
          <span className="dropzone__icon">
            <svg
              viewBox="0 0 24 24"
              aria-hidden="true"
              fill="none"
              stroke="currentColor"
              strokeWidth="1.5"
            >
              <path
                strokeLinecap="round"
                strokeLinejoin="round"
                d="M12 16V4m0 0L7 9m5-5 5 5M4 20h16"
              />
            </svg>
          </span>
          <span className="dropzone__title">Drop a video</span>
          <span className="dropzone__hint">MP4, MOV, or WebM — it never leaves your device</span>
        </span>
      </button>
    </>
  );
}
