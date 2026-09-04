"use client";

import Link from "next/link";
import { Hexagon } from "lucide-react";
import { FormEvent, useState } from "react";
import { api } from "@/lib/api";

export function LoginForm({ mode }: { mode: "login" | "setup" }) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const setup = mode === "setup";

  async function submit(event: FormEvent) {
    event.preventDefault(); setSubmitting(true); setError(null);
    try {
      if (setup) await api.setup(email, password); else await api.login(email, password);
      window.location.assign("/");
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : "Authentication failed.");
    } finally { setSubmitting(false); }
  }

  return (
    <main className="auth-shell">
      <section className="auth-panel">
        <Link className="auth-brand" href="/"><Hexagon aria-hidden="true" />NERVE</Link>
        <form className="auth-form" onSubmit={submit}>
          <h1>{setup ? "Create administrator" : "Sign in"}</h1>
          <p>{setup ? "Initialize the first local administrator account." : "Use your local NERVE administrator account."}</p>
          {error && <div className="form-error" role="alert">{error}</div>}
          <div className="field"><label htmlFor="email">Email</label><input className="input" id="email" type="email" autoComplete="username" required value={email} onChange={(event) => setEmail(event.target.value)} /></div>
          <div className="field"><label htmlFor="password">Password</label><input className="input" id="password" type="password" autoComplete={setup ? "new-password" : "current-password"} minLength={12} required value={password} onChange={(event) => setPassword(event.target.value)} /></div>
          <button className="button button-primary" type="submit" disabled={submitting}>{submitting ? "Working…" : setup ? "Create administrator" : "Sign in"}</button>
          <div className="auth-foot">{setup ? <>Already initialized? <Link href="/login">Sign in</Link></> : <>New installation? <Link href="/setup">Create the first administrator</Link></>}</div>
        </form>
      </section>
      <aside className="auth-context" aria-label="Infrastructure relationship example">
        <div className="auth-diagram">
          <span className="diagram-line line-a" /><span className="diagram-line line-b" /><span className="diagram-line line-c" />
          <div className="diagram-node node-a"><strong>Gateway</strong><span>192.168.178.1</span></div>
          <div className="diagram-node node-b"><strong>Core switch</strong><span>CONNECTED_TO</span></div>
          <div className="diagram-node node-c"><strong>Proxmox</strong><span>HOSTS · 4 VMs</span></div>
          <div className="diagram-node node-d"><strong>Docker</strong><span>RUNS · 12 containers</span></div>
        </div>
      </aside>
    </main>
  );
}

