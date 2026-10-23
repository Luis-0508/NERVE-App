"use client";

import Link from "next/link";
import { useCallback, useEffect, useState } from "react";
import { APIError, api } from "@/lib/api";
import type { DeviceDetail, DeviceVocabulary, DuplicateCandidate, IdentityReport, TrustState } from "@/lib/types";

const trustLabels: Record<TrustState, string> = { unknown: "Unknown", reviewed: "Reviewed", trusted: "Trusted", blocked: "Blocked" };
const matchLabels: Record<string, string> = {
  mac: "MAC address", "mac-ambiguous": "MAC shared by several records", "address-binding": "Previously seen at this address",
  "agent-reported-address": "Address reported by the device's agent", "new-device": "First seen (new record)",
  "address-reused-by-other-hardware": "Address reused by other hardware (new record)", unrecorded: "Before match recording",
};

function formatDate(value?: string) {
  if (!value) return "—";
  return new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(new Date(value));
}

export function TrustBadge({ state }: { state: TrustState }) {
  return <span className="trust-badge" data-trust={state}>{trustLabels[state] ?? state}</span>;
}

/** Identity tab: human-owned metadata, trust, evidence, duplicates, merge and split. */
export function DeviceIdentityPanel({ detail, permissions, onChanged }: { detail: DeviceDetail; permissions: string[]; onChanged: () => void }) {
  const device = detail.device;
  const canEdit = permissions.includes("devices.manage") && !device.mergedInto;
  const canTrust = permissions.includes("devices.trust") && !device.mergedInto;
  const canMerge = permissions.includes("devices.merge") && !device.mergedInto;
  const [identity, setIdentity] = useState<IdentityReport | null>(null);
  const [duplicates, setDuplicates] = useState<DuplicateCandidate[]>([]);
  const [vocabulary, setVocabulary] = useState<DeviceVocabulary>({ trustStates: ["unknown", "reviewed", "trusted", "blocked"], classifications: [] });
  const [form, setForm] = useState({ displayName: "", classification: "", tags: "", owner: "", location: "", notes: "" });
  const [trust, setTrust] = useState<TrustState>(device.trustState);
  const [trustReason, setTrustReason] = useState("");
  const [message, setMessage] = useState<{ kind: "error" | "ok"; text: string } | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    setForm({ displayName: device.displayName ?? "", classification: device.classificationOverridden ? device.classification : "",
      tags: device.tags.join(", "), owner: device.owner ?? "", location: device.location ?? "", notes: device.notes ?? "" });
  // Reset only when the stored values change (a save, or another operator's edit),
  // not on every reload, so unsaved edits in other fields survive a refresh.
  // eslint-disable-next-line react-hooks/exhaustive-deps
    setTrust(device.trustState);
  }, [device.id, device.metadataUpdatedAt, device.trustChangedAt]);

  const load = useCallback(async () => {
    try {
      const [report, candidates, words] = await Promise.all([api.deviceIdentity(device.id), api.deviceDuplicates(device.id), api.deviceVocabulary()]);
      setIdentity(report); setDuplicates(candidates.items); setVocabulary(words);
    } catch (error) {
      setMessage({ kind: "error", text: error instanceof Error ? error.message : "Identity evidence could not be loaded." });
    }
  }, [device.id]);
  useEffect(() => { void load(); }, [load]);

  async function act(action: () => Promise<unknown>, success: string) {
    setBusy(true); setMessage(null);
    try { await action(); setMessage({ kind: "ok", text: success }); onChanged(); await load(); }
    catch (error) { setMessage({ kind: "error", text: error instanceof APIError ? error.message : "The change failed." }); }
    finally { setBusy(false); }
  }

  function saveMetadata() {
    const tags = form.tags.split(",").map((tag) => tag.trim()).filter(Boolean);
    void act(() => api.updateDeviceMetadata(device.id, { displayName: form.displayName || null, classification: form.classification || null,
      tags, owner: form.owner || null, location: form.location || null, notes: form.notes || null }), "Saved. Discovery will not overwrite these values.");
  }

  const otherDevice = (candidate: DuplicateCandidate) => candidate.deviceA.id === device.id ? candidate.deviceB : candidate.deviceA;

  // The discovered type is shown with the claim it came from; a rule's reason
  // is only shown while it still explains that claim.
  const typeFact = identity?.facts.find((fact) => fact.attribute === "classification" && fact.effective);
  const suggestion = identity?.classification;
  const typeReason = suggestion && typeFact && typeFact.source === suggestion.source && typeFact.value === suggestion.classification ? suggestion.reason : undefined;
  return <div className="identity-panel">
    {device.mergedInto && <div className="form-error" role="status">This record was merged. Its history now lives on <Link className="device-link" href={`/devices/detail?id=${encodeURIComponent(device.mergedInto)}`}>the surviving device</Link>.</div>}
    {message && <div className={message.kind === "error" ? "form-error" : "form-ok"} role={message.kind === "error" ? "alert" : "status"}>{message.text}</div>}

    <div className="detail-grid">
      <section>
        <div className="detail-section-heading"><h2>Names and ownership</h2></div>
        <dl className="property-list">
          <div><dt>Effective name</dt><dd>{device.name}</dd></div>
          <div><dt>Discovered name</dt><dd className="mono">{device.discoveredName || "—"}</dd></div>
          <div><dt>Discovered hostname</dt><dd className="mono">{device.hostname || "—"}</dd></div>
          <div><dt>Discovered type</dt><dd>{device.discoveredClassification}{typeFact ? ` · ${typeFact.source}, confidence ${typeFact.confidence}` : identity && device.discoveredClassification === "unknown" ? " · no rule matched" : ""}{typeReason && <span className="muted classification-reason">{typeReason}</span>}</dd></div>
          {device.metadataUpdatedAt && <div><dt>Last edited</dt><dd>{formatDate(device.metadataUpdatedAt)} · {device.metadataUpdatedBy || "operator"}</dd></div>}
        </dl>
        <form className="identity-form" onSubmit={(event) => { event.preventDefault(); saveMetadata(); }}>
          <label className="field"><span>Name (manual, overrides discovery)</span><input className="input" value={form.displayName} maxLength={200} disabled={!canEdit} placeholder={device.discoveredName} onChange={(event) => setForm({ ...form, displayName: event.target.value })} /></label>
          <label className="field"><span>Type override</span><select className="input" value={form.classification} disabled={!canEdit} onChange={(event) => setForm({ ...form, classification: event.target.value })}><option value="">Use discovered ({device.discoveredClassification})</option>{vocabulary.classifications.map((item) => <option key={item} value={item}>{item}</option>)}</select></label>
          <label className="field"><span>Tags (comma separated)</span><input className="input" value={form.tags} disabled={!canEdit} onChange={(event) => setForm({ ...form, tags: event.target.value })} /></label>
          <label className="field"><span>Owner</span><input className="input" value={form.owner} maxLength={200} disabled={!canEdit} onChange={(event) => setForm({ ...form, owner: event.target.value })} /></label>
          <label className="field"><span>Location</span><input className="input" value={form.location} maxLength={200} disabled={!canEdit} onChange={(event) => setForm({ ...form, location: event.target.value })} /></label>
          <label className="field"><span>Notes</span><textarea className="input identity-notes" value={form.notes} maxLength={10000} disabled={!canEdit} onChange={(event) => setForm({ ...form, notes: event.target.value })} /></label>
          {canEdit && <button className="button button-primary" type="submit" disabled={busy}>Save</button>}
        </form>
      </section>
      <section>
        <div className="detail-section-heading"><h2>Trust</h2></div>
        <dl className="property-list">
          <div><dt>Trust state</dt><dd><TrustBadge state={device.trustState} /></dd></div>
          <div><dt>Changed</dt><dd>{device.trustChangedAt ? `${formatDate(device.trustChangedAt)} · ${device.trustChangedBy || "operator"}` : "Never (discovery never changes trust)"}</dd></div>
        </dl>
        {canTrust && <div className="identity-form">
          <label className="field"><span>New trust state</span><select className="input" value={trust} onChange={(event) => setTrust(event.target.value as TrustState)}>{vocabulary.trustStates.map((state) => <option key={state} value={state}>{trustLabels[state]}</option>)}</select></label>
          <label className="field"><span>Reason (audited)</span><input className="input" value={trustReason} maxLength={500} onChange={(event) => setTrustReason(event.target.value)} /></label>
          <button className="button" type="button" disabled={busy || trust === device.trustState} onClick={() => void act(() => api.setDeviceTrust(device.id, trust, trustReason), "Trust state changed.")}>Change trust</button>
          <p className="muted identity-hint">Blocked devices receive no remote actions. Future privileged actions will require Trusted.</p>
        </div>}
        <div className="detail-section-heading"><h2>Management agents</h2></div>
        {identity && identity.agents.length ? <dl className="property-list">{identity.agents.map((agent) => <div key={agent.id}><dt>{agent.displayName}</dt><dd>{agent.status === "active" ? (agent.online ? "Online" : "Offline") : agent.status} · association {agent.association}</dd></div>)}</dl>
          : <p className="remote-note muted">No management agent is associated with this device.</p>}
      </section>
    </div>

    <section>
      <div className="detail-section-heading"><h2>Identity evidence{identity ? ` · ${identity.strength}` : ""}</h2></div>
      {identity && <p className="remote-note">{identity.summary}</p>}
      {identity && identity.evidence.length > 0 && <div className="table-wrap"><table className="data-table"><thead><tr><th>Evidence</th><th>Value</th><th>Strength</th><th>Source</th><th>Last seen</th><th>Note</th></tr></thead><tbody>
        {identity.evidence.map((item, index) => <tr key={`${item.type}:${item.value}:${index}`}>
          <td>{item.type}</td><td className="mono">{item.value}</td><td><span className="strength" data-strength={item.strength}>{item.strength}</span></td>
          <td>{item.source}{item.manuallyConfirmed ? " · confirmed" : ""}</td><td>{formatDate(item.lastSeenAt)}</td>
          <td>{item.note}{item.sharedWith.length > 0 && <> · also on {item.sharedWith.map((other, position) => <span key={other.id}>{position > 0 && ", "}<Link className="device-link" href={`/devices/detail?id=${encodeURIComponent(other.id)}`}>{other.name}</Link></span>)}</>}</td>
        </tr>)}
      </tbody></table></div>}
      {identity && identity.discoveryMatches.length > 0 && <p className="remote-note muted">Discovery (30 days): {identity.discoveryMatches.map((match) => `${matchLabels[match.reason] ?? match.reason} ×${match.count}`).join(" · ")}</p>}
    </section>

    {identity && identity.facts.length > 0 && <section>
      <div className="detail-section-heading"><h2>Discovered facts and their sources</h2></div>
      <div className="table-wrap"><table className="data-table"><thead><tr><th>Attribute</th><th>Value</th><th>Source</th><th>Confidence</th><th>Last seen</th><th>Used</th></tr></thead><tbody>
        {identity.facts.map((fact) => <tr key={`${fact.attribute}:${fact.source}`}><td>{fact.attribute}</td><td className="mono">{fact.value}</td><td>{fact.source}</td><td>{fact.confidence}</td><td>{formatDate(fact.lastSeenAt)}</td><td>{fact.effective ? "Yes" : "—"}</td></tr>)}
      </tbody></table></div>
    </section>}

    <section>
      <div className="detail-section-heading"><h2>Possible duplicates</h2></div>
      {duplicates.length === 0 ? <p className="remote-note muted">No other record shares strong identity evidence with this device.</p>
        : duplicates.map((candidate) => {
          const other = otherDevice(candidate);
          return <div className="duplicate-row" key={other.id}>
            <div><strong><Link className="device-link" href={`/devices/detail?id=${encodeURIComponent(other.id)}`}>{other.name}</Link></strong> · {candidate.confidence} confidence<br /><span className="muted">{candidate.reasons.join("; ")}</span></div>
            {canMerge && <div className="agent-actions">
              <button className="button" type="button" disabled={busy} onClick={() => { if (window.confirm(`Merge "${other.name}" into "${device.name}"? Its history moves here and the other record is retired.`)) void act(() => api.mergeDevice(device.id, other.id, "duplicate candidate"), "Merged into this device."); }}>Merge into this</button>
              <button className="button" type="button" disabled={busy} onClick={() => { if (window.confirm(`Merge "${device.name}" into "${other.name}"? This record will be retired.`)) void act(() => api.mergeDevice(other.id, device.id, "duplicate candidate"), "Merged into the other device."); }}>Merge into other</button>
              <button className="button" type="button" disabled={busy} onClick={() => void act(() => api.dismissDuplicate(device.id, other.id, "operator: different devices"), "Marked as different devices.")}>Not a duplicate</button>
            </div>}
          </div>;
        })}
    </section>

    {canMerge && detail.interfaces.length > 1 && <SplitForm detail={detail} hasAgent={Boolean(identity?.agents.some((agent) => agent.status !== "removed"))} busy={busy}
      onSplit={(input) => act(() => api.splitDevice(device.id, input), "Split complete. The selected interfaces now belong to a new device.")} />}
  </div>;
}

function SplitForm({ detail, hasAgent, busy, onSplit }: { detail: DeviceDetail; hasAgent: boolean; busy: boolean; onSplit: (input: { displayName: string; interfaceIds: string[]; agent?: string; reason: string }) => void }) {
  const [selected, setSelected] = useState<string[]>([]);
  const [name, setName] = useState("");
  const [agent, setAgent] = useState("");
  return <section>
    <div className="detail-section-heading"><h2>Split device</h2></div>
    <div className="identity-form">
      <p className="muted identity-hint">Move interfaces that belong to a different physical device into a new record. Their addresses, discovery bindings and MAC evidence move with them; observations stay here.</p>
      {detail.interfaces.map((item) => <label key={item.id} className="checkbox-row"><input type="checkbox" checked={selected.includes(item.id)}
        onChange={(event) => setSelected(event.target.checked ? [...selected, item.id] : selected.filter((id) => id !== item.id))} /> {item.name} <span className="mono muted">{item.macAddress} {item.addresses.map((address) => address.address).join(", ")}</span></label>)}
      <label className="field"><span>Name of the new device</span><input className="input" value={name} maxLength={200} onChange={(event) => setName(event.target.value)} /></label>
      {hasAgent && <label className="field"><span>Management agent</span><select className="input" value={agent} onChange={(event) => setAgent(event.target.value)}><option value="">Choose…</option><option value="keep">Keep on this device</option><option value="move">Move to the new device</option><option value="detach">Detach from both</option></select></label>}
      <button className="button" type="button" disabled={busy || selected.length === 0 || selected.length === detail.interfaces.length || (hasAgent && !agent)}
        onClick={() => { if (window.confirm("Split the selected interfaces into a new device?")) onSplit({ displayName: name, interfaceIds: selected, agent: hasAgent ? agent || undefined : undefined, reason: "operator split" }); }}>Split</button>
    </div>
  </section>;
}
