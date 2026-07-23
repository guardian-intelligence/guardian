import { createFileRoute } from "@tanstack/react-router";
import "~/styles/postflight.css";

export const Route = createFileRoute("/postflight/device/done")({
  component: DeviceDonePage,
});

function DeviceDonePage() {
  return (
    <main className="postflight-device-page">
      <div className="postflight-login-card postflight-device-card">
        <h2>CLI connected</h2>
        <p data-device-done>
          Your terminal is signed in. You can close this tab and return to the command line.
        </p>
      </div>
    </main>
  );
}
