import type { CSSProperties } from "react";
import { Toaster as Sonner, type ToasterProps } from "sonner";

export function Toaster(props: ToasterProps) {
  return (
    <Sonner
      theme="dark"
      position="bottom-center"
      closeButton
      className="toaster group"
      style={
        {
          "--normal-bg": "#111118",
          "--normal-text": "#f4f3ff",
          "--normal-border": "rgba(255, 255, 255, 0.14)",
          "--border-radius": "0.75rem",
        } as CSSProperties
      }
      {...props}
    />
  );
}
