import { createFileRoute } from "@tanstack/react-router";

export const Route = createFileRoute("/")({
  component: Home,
  head: () => ({
    meta: [
      { title: "Anveio Foundation" },
      { name: "description", content: "Anveio Foundation, a Delaware corporation." },
    ],
  }),
});

export function Home() {
  return (
    <main className="home">
      <p className="home-mark">Anveio Foundation</p>
      <p className="home-line">A Delaware corporation.</p>
    </main>
  );
}
