import type { WebAuthnCreateOptions, WebAuthnGetOptions } from "./webauthn";

export class ApiError extends Error {
  status: number;
  output?: string;
  constructor(status: number, message: string, output?: string) {
    super(message);
    this.status = status;
    this.output = output;
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    credentials: "same-origin",
    headers: init?.body ? { "Content-Type": "application/json" } : undefined,
    ...init,
  });
  const isJSON = res.headers.get("Content-Type")?.includes("application/json");
  const body = isJSON ? await res.json() : null;
  if (!res.ok) {
    throw new ApiError(res.status, body?.error ?? res.statusText, body?.output);
  }
  return body as T;
}

export interface TreeEntry {
  name: string;
  path: string;
  isDir: boolean;
  size?: number;
  symlink?: string;
  external?: boolean;
  children?: TreeEntry[];
}

export interface LogFile {
  path: string;
  size: number;
  modTime: string;
  gzip: boolean;
}

export interface LogChunk {
  lines: string[];
  offset: number;
  size: number;
  atEnd: boolean;
}

export interface NginxStatus {
  running: boolean;
  version: string;
  supervise: boolean;
}

export interface ActionResult {
  status?: string;
  ok?: boolean;
  output?: string;
}

export interface Site {
  name: string;
  enabled: boolean;
  maintenance: boolean;
  domains: string[] | null;
}

export interface SitesResponse {
  available: boolean;
  maintenance: boolean;
  sites: Site[];
}

export type SiteAction = "enable" | "disable" | "maintenance_on" | "maintenance_off" | "delete";
export type VhostKind = "sites" | "streams";

export type Level = "full" | "mfa" | "enroll";
export type Role = "admin" | "user";

export interface Me {
  user: string;
  role: Role;
  method: "local" | "oidc";
  level: Level;
  mfa?: { totp: boolean; webauthn: boolean };
  mfaRequired?: boolean;
  policy?: MFAPolicy;
}

export interface MFAPolicy {
  decided: boolean;
  pinned: boolean;
  requiredRoles: Role[] | null;
}

export interface User {
  id: number;
  username: string;
  role: Role;
  totpEnrolled: boolean;
  webauthnCount: number;
  createdAt: string;
  updatedAt: string;
}

export interface Credential {
  id: string;
  name: string;
  createdAt: string;
}

export interface APIKey {
  id: number;
  name: string;
  prefix: string;
  scopes: string[];
  createdAt: string;
  lastUsedAt?: string | null;
}

export const api = {
  authStatus: () =>
    request<{ bootstrap: boolean; local: boolean; oidc: boolean; oidcLabel?: string }>(
      "/api/auth/status",
    ),
  setup: (username: string, password: string) =>
    request<{ user: string; level: Level }>("/api/auth/setup", {
      method: "POST",
      body: JSON.stringify({ username, password }),
    }),
  me: () => request<Me>("/api/me"),
  login: (username: string, password: string) =>
    request<{ user: string; level: Level }>("/api/auth/login", {
      method: "POST",
      body: JSON.stringify({ username, password }),
    }),
  logout: () => request("/api/auth/logout", { method: "POST" }),

  // MFA at login (partial "mfa" session -> full)
  verifyTOTP: (code: string) =>
    request<{ level: Level }>("/api/auth/mfa/verify/totp", {
      method: "POST",
      body: JSON.stringify({ code }),
    }),
  verifyWebAuthnBegin: () =>
    request<WebAuthnGetOptions>("/api/auth/mfa/verify/webauthn/begin", {
      method: "POST",
      body: "{}",
    }),
  verifyWebAuthnFinish: (cred: unknown) =>
    request<{ level: Level }>("/api/auth/mfa/verify/webauthn/finish", {
      method: "POST",
      body: JSON.stringify(cred),
    }),

  // MFA enrolment (forced "enroll" session, or profile)
  totpBegin: () =>
    request<{ secret: string; uri: string }>("/api/mfa/totp/begin", { method: "POST", body: "{}" }),
  totpConfirm: (code: string) =>
    request<{ status: string; level?: Level }>("/api/mfa/totp/confirm", {
      method: "POST",
      body: JSON.stringify({ code }),
    }),
  webauthnRegisterBegin: () =>
    request<WebAuthnCreateOptions>("/api/mfa/webauthn/register/begin", {
      method: "POST",
      body: "{}",
    }),
  webauthnRegisterFinish: (name: string, cred: unknown) =>
    request<{ status: string; level?: Level }>(
      `/api/mfa/webauthn/register/finish?name=${encodeURIComponent(name)}`,
      { method: "POST", body: JSON.stringify(cred) },
    ),

  // Profile
  credentials: () => request<{ credentials: Credential[] }>("/api/mfa/webauthn"),
  deleteCredential: (id: string) =>
    request(`/api/mfa/webauthn?id=${encodeURIComponent(id)}`, { method: "DELETE" }),
  deleteTOTP: () => request("/api/mfa/totp", { method: "DELETE" }),
  changePassword: (current: string, next: string) =>
    request("/api/account/password", {
      method: "POST",
      body: JSON.stringify({ current, new: next }),
    }),

  // Admin
  getPolicy: () => request<MFAPolicy>("/api/admin/mfa-policy"),
  setPolicy: (requiredRoles: Role[]) =>
    request<MFAPolicy>("/api/admin/mfa-policy", {
      method: "POST",
      body: JSON.stringify({ requiredRoles }),
    }),
  users: () => request<{ users: User[] }>("/api/admin/users"),
  createUser: (username: string, password: string, role: Role) =>
    request<User>("/api/admin/users", {
      method: "POST",
      body: JSON.stringify({ username, password, role }),
    }),
  updateUser: (id: number, patch: { role?: Role; password?: string }) =>
    request<User>(`/api/admin/users/${id}`, { method: "PATCH", body: JSON.stringify(patch) }),
  resetUserMFA: (id: number) =>
    request<User>(`/api/admin/users/${id}/reset-mfa`, { method: "POST", body: "{}" }),
  deleteUser: (id: number) => request(`/api/admin/users/${id}`, { method: "DELETE" }),

  apiKeys: () => request<{ keys: APIKey[]; scopes: string[] }>("/api/admin/api-keys"),
  createApiKey: (name: string, scopes: string[]) =>
    request<{ key: APIKey; token: string }>("/api/admin/api-keys", {
      method: "POST",
      body: JSON.stringify({ name, scopes }),
    }),
  deleteApiKey: (id: number) => request(`/api/admin/api-keys/${id}`, { method: "DELETE" }),

  tree: () => request<{ root: string; tree: TreeEntry }>("/api/config/tree"),
  readFile: (path: string) =>
    request<{ path: string; content: string }>(
      `/api/config/file?path=${encodeURIComponent(path)}`,
    ),
  writeFile: (path: string, content: string) =>
    request<ActionResult>("/api/config/file", {
      method: "PUT",
      body: JSON.stringify({ path, content }),
    }),
  deleteFile: (path: string) =>
    request<ActionResult>(`/api/config/file?path=${encodeURIComponent(path)}`, {
      method: "DELETE",
    }),

  status: () => request<NginxStatus>("/api/nginx/status"),
  test: () => request<ActionResult>("/api/nginx/test", { method: "POST" }),
  reload: () => request<ActionResult>("/api/nginx/reload", { method: "POST" }),
  restart: () => request<ActionResult>("/api/nginx/restart", { method: "POST" }),

  vhosts: (kind: VhostKind) => request<SitesResponse>(`/api/vhosts/${kind}`),
  vhostAction: (kind: VhostKind, names: string[], action: SiteAction) =>
    request<ActionResult>(`/api/vhosts/${kind}/action`, {
      method: "POST",
      body: JSON.stringify({ names, action }),
    }),
  vhostRename: (kind: VhostKind, name: string, newName: string) =>
    request<ActionResult>(`/api/vhosts/${kind}/rename`, {
      method: "POST",
      body: JSON.stringify({ name, newName }),
    }),
  renameFile: (from: string, to: string) =>
    request<ActionResult>("/api/config/rename", {
      method: "POST",
      body: JSON.stringify({ from, to }),
    }),
  mkdir: (path: string) =>
    request<{ status: string }>("/api/config/mkdir", {
      method: "POST",
      body: JSON.stringify({ path }),
    }),

  logs: () => request<{ files: LogFile[] }>("/api/logs"),
  logRead: (path: string, before?: number, bytes?: number) => {
    const q = new URLSearchParams({ path });
    if (before) q.set("before", String(before));
    if (bytes) q.set("bytes", String(bytes));
    return request<LogChunk>(`/api/logs/read?${q}`);
  },
  logStreamURL: (path: string, from?: number) => {
    const q = new URLSearchParams({ path });
    if (from !== undefined) q.set("from", String(from));
    return `/api/logs/stream?${q}`;
  },
};
