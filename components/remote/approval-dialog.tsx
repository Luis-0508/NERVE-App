"use client";

import { useEffect, useRef, useState } from "react";
import { APIError, api } from "@/lib/api";
import type { RemoteCommand } from "@/lib/types";

const trustLabels: Record<string, string> = { unknown: "Unknown", reviewed: "Reviewed", trusted: "Trusted", blocked: "Blocked" };
const associationLabels: Record<string, string> = {
  automatic: "Automatic (unique MAC match)", confirmed: "Confirmed by an operator", candidate: "Candidate only",
  unassociated: "Not associated", dismissed: "Dismissed by an operator",
};

// ApprovalDialog is the second step for a privileged command: the operator
// sees who the command will reach, and why NERVE links that agent to that
// device, before Core signs an approval and queues it.
export function ApprovalDialog({ command, onClose, onApproved }: {
  command: RemoteCommand;
  onClose: () => void;
  onApproved: (command: RemoteCommand) => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const target = command.target;

  useEffect(() => {
    const element = dialog.current;
    if (element && !element.open) element.showModal();
    return () => element?.close();
  }, []);

  async function approve() {
    setBusy(true); setError(null);
    try {
      onApproved(await api.approveCommand(command.id));
    } catch (requestError) {
      setError(requestError instanceof APIError ? requestError.message : "The command could not be approved.");
      setBusy(false);
    }
  }

  const parameters = Object.entries(command.parameters ?? {});
  return <dialog ref={dialog} className="approval-dialog" aria-labelledby="approval-title" onCancel={(event) => { event.preventDefault(); if (!busy) onClose(); }}>
    <h2 id="approval-title">Approve {command.type}?</h2>
    <p className="approval-lead">Approving queues this command for the agent below. The agent runs it only with Core&apos;s signed approval, which expires in 10 minutes or when the command does.</p>
    {target ? <dl className="property-list">
      <div><dt>Device</dt><dd>{target.deviceName || "—"}</dd></div>
      <div><dt>Trust</dt><dd>{trustLabels[target.trustState ?? ""] ?? (target.trustState || "—")}</dd></div>
      <div><dt>Agent</dt><dd>{target.agentName}{target.hostname ? ` (${target.hostname})` : ""}</dd></div>
      <div><dt>Agent key</dt><dd className="mono approval-fingerprint">{target.agentFingerprint || "—"}</dd></div>
      <div><dt>Connection</dt><dd>{target.transport === "https" ? "HTTPS, certificate verified" : target.transport === "http" ? "Unencrypted HTTP" : "Not reported"}</dd></div>
      <div><dt>Association</dt><dd>{associationLabels[target.associationStatus] ?? target.associationStatus}</dd></div>
    </dl> : <p className="form-error">The target could not be loaded. Do not approve without it.</p>}
    {target && target.associationEvidence.length > 0 && <div className="approval-evidence">
      <h3>Why this agent belongs to this device</h3>
      <ul>{target.associationEvidence.map((item, index) => <li key={`${item.deviceId}-${index}`}>
        {item.signals.map((signal) => `${signal.type} ${signal.value}`).join(", ") || "No signals"} ({item.strength}){item.note ? `: ${item.note}` : ""}
      </li>)}</ul>
    </div>}
    <div className="approval-evidence">
      <h3>Parameters</h3>
      {parameters.length ? <dl className="property-list">{parameters.map(([key, value]) => <div key={key}><dt>{key}</dt><dd className="mono">{JSON.stringify(value)}</dd></div>)}</dl>
        : <p className="muted">None</p>}
    </div>
    {error && <div className="form-error" role="alert">{error}</div>}
    <div className="approval-actions">
      <button className="button" type="button" onClick={onClose} disabled={busy}>Keep waiting</button>
      <button className="button button-primary" type="button" onClick={() => void approve()} disabled={busy || !target} aria-busy={busy}>Approve and queue</button>
    </div>
  </dialog>;
}
