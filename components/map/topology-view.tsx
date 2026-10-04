"use client";

import { Maximize, Tags } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import type { Topology } from "./topology";
import { TopologyScene } from "./topology-scene";

type Props = { topology: Topology; paused: boolean; panelOpen: boolean; selectedNodeId: string | null; selectedFlowId: string | null; onSelectNode: (nodeId: string | null) => void };

export default function TopologyView({ topology, paused, panelOpen, selectedNodeId, selectedFlowId, onSelectNode }: Props) {
  const host = useRef<HTMLDivElement>(null);
  const scene = useRef<TopologyScene | null>(null);
  const select = useRef(onSelectNode);
  const [failure, setFailure] = useState<string | null>(null);
  const [labels, setLabels] = useState(true);
  useEffect(() => { select.current = onSelectNode; }, [onSelectNode]);
  useEffect(() => {
    try { setLabels(localStorage.getItem("nerve:topology-labels") !== "off"); } catch { /* Labels stay on. */ }
    try {
      scene.current = new TopologyScene(host.current!, {
        onSelect: id => select.current(id),
        onContextLost: () => setFailure("The graphics context was lost. Reload the page to restore the 3D map."),
      });
    } catch { setFailure("3D rendering is unavailable in this browser. Use the World view instead."); }
    return () => { scene.current?.dispose(); scene.current = null; };
  }, []);
  useEffect(() => { scene.current?.setTopology(topology); }, [topology]);
  useEffect(() => { scene.current?.setSelection(selectedNodeId, selectedFlowId); }, [selectedNodeId, selectedFlowId, topology]);
  useEffect(() => { scene.current?.setPaused(paused); }, [paused]);
  useEffect(() => { scene.current?.setInset(panelOpen ? 356 : 0); }, [panelOpen]);
  useEffect(() => { scene.current?.setLabels(labels); }, [labels]);
  function toggleLabels() {
    setLabels(value => { try { localStorage.setItem("nerve:topology-labels", value ? "off" : "on"); } catch { /* Per-visit only. */ } return !value; });
  }
  return <div className="topology-view" onKeyDown={e => { if (e.key === "Escape") onSelectNode(null); }}>
    <div ref={host} className="topology-host"/>
    {failure ? <div className="traffic-empty" role="alert"><strong>{failure}</strong></div> : <div className="topology-toolbar" role="toolbar" aria-label="3D map view">
      <button type="button" className="icon-button" onClick={() => scene.current?.fit()} title="Fit topology to view" aria-label="Fit topology to view"><Maximize aria-hidden="true"/></button>
      <button type="button" className="icon-button" onClick={toggleLabels} aria-pressed={labels} title={labels ? "Hide device labels" : "Show device labels"} aria-label="Device labels"><Tags aria-hidden="true"/></button>
    </div>}
  </div>;
}
