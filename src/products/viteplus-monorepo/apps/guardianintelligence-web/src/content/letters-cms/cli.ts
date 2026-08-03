// letters-cms: grant a person SSO access to the letters Studio.
//
//   node src/content/letters-cms/cli.ts setup-sso <email>
//
// Everything else about Directus (collection, fields, the anonymous read
// grant the site depends on) is reconciled from git by the
// directus-provisioner Job (deployments/products/prod/directus-provisioner.yaml).
// Editors stay a manual admin action because their emails are personal data
// that does not belong in the repo. Idempotent — safe to re-run.
//
// Auth (run against a port-forward, `kubectl port-forward svc/directus 8055:80`):
//   DIRECTUS_URL      default http://127.0.0.1:8055
//   DIRECTUS_TOKEN    static token, or:
//   DIRECTUS_EMAIL / DIRECTUS_PASSWORD  admin login

export {};

const BASE_URL = process.env["DIRECTUS_URL"] ?? "http://127.0.0.1:8055";
const COLLECTION = "letters";

let accessToken: string | null = process.env["DIRECTUS_TOKEN"] ?? null;

async function login(): Promise<void> {
  if (accessToken) return;
  const email = process.env["DIRECTUS_EMAIL"];
  const password = process.env["DIRECTUS_PASSWORD"];
  if (!email || !password) {
    throw new Error("letters-cms: set DIRECTUS_TOKEN or DIRECTUS_EMAIL/DIRECTUS_PASSWORD");
  }
  const res = await fetch(`${BASE_URL}/auth/login`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ email, password }),
  });
  if (!res.ok) throw new Error(`letters-cms: login failed: ${res.status} ${await res.text()}`);
  const json = (await res.json()) as { data: { access_token: string } };
  accessToken = json.data.access_token;
}

async function api(path: string, init?: RequestInit): Promise<Response> {
  return fetch(`${BASE_URL}${path}`, {
    ...init,
    headers: {
      authorization: `Bearer ${accessToken}`,
      "content-type": "application/json",
      ...init?.headers,
    },
  });
}

async function find(path: string): Promise<{ id: string } | undefined> {
  const res = await api(path);
  if (!res.ok) throw new Error(`letters-cms: ${path} failed: ${res.status} ${await res.text()}`);
  const json = (await res.json()) as { data: { id: string }[] };
  return json.data[0];
}

async function create(path: string, body: unknown): Promise<string> {
  const res = await api(path, { method: "POST", body: JSON.stringify(body) });
  if (!res.ok)
    throw new Error(`letters-cms: POST ${path} failed: ${res.status} ${await res.text()}`);
  const json = (await res.json()) as { data: { id: string } };
  return json.data.id;
}

// Idempotently provisions the scoped SSO editor: a letters-only policy and
// role (app access, no admin), and a pre-created Keycloak-provider user so
// the email can sign in through SSO while public registration stays off.
async function setupSso(email: string): Promise<void> {
  let role = await find(`/roles?filter[name][_eq]=Letters%20Editor&fields=id`);
  if (!role) role = { id: await create("/roles", { name: "Letters Editor", icon: "edit_note" }) };
  console.error(`role Letters Editor: ${role.id}`);

  let policy = await find(`/policies?filter[name][_eq]=letters-editor&fields=id`);
  if (!policy) {
    policy = {
      id: await create("/policies", {
        name: "letters-editor",
        icon: "edit_note",
        app_access: true,
        admin_access: false,
        roles: [{ role: role.id }],
      }),
    };
  }
  console.error(`policy letters-editor: ${policy.id}`);

  for (const action of ["create", "read", "update", "delete"]) {
    const existing = await find(
      `/permissions?filter[policy][_eq]=${policy.id}&filter[collection][_eq]=${COLLECTION}&filter[action][_eq]=${action}&fields=id`,
    );
    if (!existing) {
      await create("/permissions", {
        policy: policy.id,
        collection: COLLECTION,
        action,
        fields: ["*"],
      });
    }
    console.error(`permission ${COLLECTION}.${action}: ok`);
  }

  const user = await find(`/users?filter[email][_eq]=${encodeURIComponent(email)}&fields=id`);
  if (!user) {
    await create("/users", {
      email,
      role: role.id,
      provider: "keycloak",
      external_identifier: email,
    });
    console.error(`user ${email}: created (keycloak provider, Letters Editor)`);
  } else {
    console.error(`user ${email}: exists`);
  }
}

const command = process.argv[2];
await login();
if (command === "setup-sso" && process.argv[3]) await setupSso(process.argv[3]);
else {
  console.error("usage: node src/content/letters-cms/cli.ts setup-sso <email>");
  process.exit(2);
}
