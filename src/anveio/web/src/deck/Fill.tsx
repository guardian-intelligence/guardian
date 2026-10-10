// Renders [bracketed] runs as visible placeholders until real figures land.
export function Fill({ text }: { text: string }) {
  return text
    .split(/(\[[^\]]*\])/)
    .map((part, i) => (part.startsWith("[") ? <mark key={i}>{part}</mark> : part));
}
