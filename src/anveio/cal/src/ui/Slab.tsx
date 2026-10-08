import type { ComponentPropsWithoutRef } from "react";

// A slab of jet black behind a thin reflective glass edge: the inside never
// takes light, only the edge catches it, brightest along the top. Rumi's
// bubble, her typing indicator and the cards in her stack are all slabs.
// `faint` dims the edge for a slab set inside another one.

type Edge = { faint?: boolean };

const cls = (faint: boolean | undefined, className: string | undefined) =>
  ["slab", faint && "slab-faint", className].filter(Boolean).join(" ");

export function Slab({ faint, className, ...rest }: ComponentPropsWithoutRef<"div"> & Edge) {
  return <div className={cls(faint, className)} {...rest} />;
}

export function SlabButton({
  faint,
  className,
  type = "button",
  ...rest
}: ComponentPropsWithoutRef<"button"> & Edge) {
  return <button type={type} className={cls(faint, className)} {...rest} />;
}
