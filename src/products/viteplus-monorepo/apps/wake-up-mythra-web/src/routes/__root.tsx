import { createRootRoute, HeadContent, Outlet, Scripts } from "@tanstack/react-router";
import { TelemetryProbe } from "@guardian/telemetry/probe";
import "~/styles/app.css";

export const Route = createRootRoute({
  component: RootComponent,
  head: () => ({
    meta: [
      { charSet: "utf-8" },
      { name: "viewport", content: "width=device-width, initial-scale=1" },
      { title: "wake up mythra" },
      {
        name: "description",
        content:
          "Wake Up, Mythra! — a shared dog park, live. Walk your dog, meet the pack, wake the samoyed.",
      },
    ],
  }),
});

function RootComponent() {
  return (
    <html lang="en">
      <head>
        <HeadContent />
      </head>
      <body>
        <TelemetryProbe app="wum" />
        <Outlet />
        <Scripts />
      </body>
    </html>
  );
}
