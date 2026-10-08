import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { App } from "./App.tsx";
import "./styles.css";

// iOS Safari only applies :active (the pressed look of rows and Rumi) once the
// page listens for touches.
document.addEventListener("touchstart", () => {}, { passive: true });

const root = document.getElementById("root");
if (!root) throw new Error("missing #root");
createRoot(root).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
