import { useEffect, useState, type FormEvent } from "react";
import { api, ApiError } from "../api";
import { useI18n } from "../i18n";
import type { ThemePref } from "../theme";
import { Btn } from "../ui";
import AuthShell from "./AuthShell";
import { AuthError, Field } from "./auth/fields";

export default function Login({
  onAuthed,
  themePref,
  setThemePref,
}: {
  onAuthed: () => void;
  themePref: ThemePref;
  setThemePref: (p: ThemePref) => void;
}) {
  const { t } = useI18n();
  const [oidc, setOidc] = useState(false);
  const [oidcLabel, setOidcLabel] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    api
      .authStatus()
      .then((s) => {
        setOidc(s.oidc);
        setOidcLabel(s.oidcLabel ?? "");
      })
      .catch(() => {});
  }, []);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api.login(username, password);
      onAuthed();
    } catch (err) {
      if (err instanceof ApiError && err.status === 429) setError(t.tooManyAttempts);
      else setError(t.loginFailed);
    } finally {
      setBusy(false);
    }
  }

  return (
    <AuthShell themePref={themePref} setThemePref={setThemePref}>
      <form onSubmit={submit} className="flex flex-col gap-3.5">
        <Field
          label={t.username}
          value={username}
          onChange={(e) => setUsername(e.target.value)}
          autoComplete="username"
          autoCapitalize="none"
          required
        />
        <Field
          label={t.password}
          type="password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          autoComplete="current-password"
          required
        />
        {error && <AuthError>{error}</AuthError>}
        <Btn type="submit" variant="primary" disabled={busy}>
          {t.signIn}
        </Btn>
      </form>

      {oidc && (
        <>
          <div className="flex items-center gap-2.5 text-xs text-dim before:h-px before:flex-1 before:bg-line after:h-px after:flex-1 after:bg-line">
            {t.or}
          </div>
          <Btn
            onClick={() => {
              window.location.href = "/api/auth/oidc/login";
            }}
          >
            {oidcLabel ? t.signInWith(oidcLabel) : t.signInOIDC}
          </Btn>
        </>
      )}
    </AuthShell>
  );
}
