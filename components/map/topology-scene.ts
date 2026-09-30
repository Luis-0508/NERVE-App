import * as THREE from "three";
import { OrbitControls } from "three/addons/controls/OrbitControls.js";
import { CSS2DObject, CSS2DRenderer } from "three/addons/renderers/CSS2DRenderer.js";
import { LineSegments2 } from "three/addons/lines/LineSegments2.js";
import { LineSegmentsGeometry } from "three/addons/lines/LineSegmentsGeometry.js";
import { LineMaterial } from "three/addons/lines/LineMaterial.js";
import { mesh } from "topojson-client";
import type { GeometryCollection, Topology as TopoJSON } from "topojson-specification";
import type { DeviceStatus } from "@/lib/types";
import { cylinderPoint, hash, neighborhood, roleOrder, type NodeRole, type Topology, type TopologyLink, type TopologyNode } from "./topology";

// Imperative three.js scene. React owns data and UI chrome; this class owns GPU resources and
// renders on demand. Only measured traffic (links, pulses, remote endpoints) emits light;
// devices, plates and the horizon are matte so structure never reads as telemetry.

const PLATE_HEIGHT = .24, DEVICE_SCALE = 1.3, CURVE_SEGMENTS = 18, MAX_PARTICLES = 3000;
const deviceShapes: Record<NodeRole | "observed", { geometry: () => THREE.BufferGeometry; height: number; footprint: number }> = {
  gateway: { geometry: () => new THREE.CylinderGeometry(.95, 1.05, .7, 6).translate(0, .35, 0), height: .7, footprint: 1.25 },
  switch: { geometry: () => new THREE.BoxGeometry(1.9, .38, .95).translate(0, .19, 0), height: .38, footprint: 1.3 },
  server: { geometry: () => new THREE.BoxGeometry(.95, 1.7, 1.05).translate(0, .85, 0), height: 1.7, footprint: .95 },
  client: { geometry: () => new THREE.BoxGeometry(.95, .6, .75).translate(0, .3, 0), height: .6, footprint: .85 },
  unknown: { geometry: () => new THREE.OctahedronGeometry(.5).translate(0, .55, 0), height: 1.05, footprint: .7 },
  observed: { geometry: () => new THREE.OctahedronGeometry(.5).translate(0, .55, 0), height: 1.05, footprint: .7 },
};
type ShapeKey = keyof typeof deviceShapes;

type Palette = Record<"bg" | "floor" | "plate" | "body" | "border" | "borderStrong" | "text" | "muted" | "accent" | "info" | "success" | "warning" | "danger", THREE.Color> & { dark: boolean };
function readPalette(element: HTMLElement): Palette {
  const style = getComputedStyle(element);
  const color = (name: string, fallback: string) => new THREE.Color().setStyle(style.getPropertyValue(name).trim() || fallback);
  const bg = color("--surface-subtle", "#f8faf8"), text = color("--text", "#17201b"), raised = color("--surface-raised", "#ffffff");
  const dark = bg.getHSL({ h: 0, s: 0, l: 0 }).l < .35;
  return {
    dark, bg, text,
    floor: dark ? bg.clone() : bg.clone().lerp(text, .045),
    plate: dark ? raised.clone().lerp(text, .05) : raised.clone(),
    body: dark ? raised.clone().lerp(text, .5) : raised.clone().lerp(text, .16),
    border: color("--border", "#dce2de"), borderStrong: color("--border-strong", "#bdc8c1"), muted: color("--text-muted", "#718078"),
    accent: color("--accent", "#177257"), info: color("--info", "#286d9c"), success: color("--success", "#138052"), warning: color("--warning", "#a1640b"), danger: color("--danger", "#c03b36"),
  };
}
function statusColor(p: Palette, status: DeviceStatus) {
  return status === "online" ? p.success : status === "warning" ? p.warning : status === "starting" ? p.info : status === "offline" ? p.danger : status === "stopped" ? p.muted : p.borderStrong;
}
const signature = (p: Palette) => [p.bg, p.text, p.accent, p.info, p.success, p.warning, p.borderStrong].map(c => c.getHexString()).join();
const inactive = (status: DeviceStatus) => status === "offline" || status === "stopped";
const intensity = (bytesPerSecond: number) => Math.min(1, Math.max(.12, Math.log10(bytesPerSecond + 1) / 7));

let coastlines: Promise<[number, number][][]> | undefined;
function loadCoastlines() {
  return coastlines ??= import("world-atlas/land-110m.json").then(module => {
    const world = ((module as { default?: unknown }).default ?? module) as TopoJSON<{ land: GeometryCollection }>;
    return mesh(world, world.objects.land).coordinates as [number, number][][];
  }).catch(error => { coastlines = undefined; throw error; });
}

class InstancedLayer {
  mesh: THREE.InstancedMesh;
  ids: string[] = [];
  constructor(private geometry: THREE.BufferGeometry, private material: THREE.Material, private parent: THREE.Object3D, private shadows: boolean) { this.mesh = this.create(16); }
  private create(capacity: number) {
    const instanced = new THREE.InstancedMesh(this.geometry, this.material, capacity);
    instanced.castShadow = this.shadows; instanced.receiveShadow = this.shadows; instanced.count = 0;
    instanced.setColorAt(0, new THREE.Color());
    this.parent.add(instanced);
    return instanced;
  }
  resize(count: number) {
    if (count > this.mesh.instanceMatrix.count) {
      this.parent.remove(this.mesh); this.mesh.dispose();
      this.mesh = this.create(2 ** Math.ceil(Math.log2(count)));
    }
    this.mesh.count = count;
  }
  commit() {
    this.mesh.instanceMatrix.needsUpdate = true;
    if (this.mesh.instanceColor) this.mesh.instanceColor.needsUpdate = true;
    this.mesh.computeBoundingSphere();
  }
  dispose() { this.parent.remove(this.mesh); this.mesh.dispose(); this.geometry.dispose(); }
}

const particleVertex = /* glsl */`
attribute vec3 aP0; attribute vec3 aP1; attribute vec3 aP2; attribute vec4 aParams; attribute vec3 aColor;
uniform float uTime; uniform float uScale;
varying vec3 vColor; varying float vAlpha;
void main() {
  float t = fract(aParams.x + uTime * aParams.y), u = 1.0 - t;
  vec3 p = u * u * aP0 + 2.0 * u * t * aP1 + t * t * aP2;
  vec4 mv = modelViewMatrix * vec4(p, 1.0);
  gl_Position = projectionMatrix * mv;
  gl_PointSize = aParams.z * uScale / -mv.z;
  vColor = aColor;
  vAlpha = aParams.w * smoothstep(0.0, 0.05, t) * smoothstep(1.0, 0.92, t);
}`;
const particleFragment = /* glsl */`
varying vec3 vColor; varying float vAlpha;
void main() {
  float d = length(gl_PointCoord - 0.5);
  float a = smoothstep(0.5, 0.08, d);
  gl_FragColor = vec4(vColor, a * a * vAlpha);
  #include <colorspace_fragment>
}`;

export type SceneCallbacks = { onSelect: (nodeId: string | null) => void; onHover?: (nodeId: string | null) => void; onContextLost?: () => void };

export class TopologyScene {
  private renderer: THREE.WebGLRenderer;
  private labelRenderer = new CSS2DRenderer();
  private scene = new THREE.Scene();
  private camera = new THREE.PerspectiveCamera(38, 1, .1, 2000);
  private controls: OrbitControls;
  private palette: Palette;
  private raycaster = new THREE.Raycaster();
  private pointer = new THREE.Vector2();
  private pointerInside = false; private pickPending = false; private pointerDown: { x: number; y: number } | null = null;

  private floor: THREE.Mesh<THREE.PlaneGeometry, THREE.MeshStandardMaterial>;
  private grid: THREE.GridHelper | null = null;
  private hemisphere = new THREE.HemisphereLight(0xffffff, 0xffffff, 1.1);
  private sun = new THREE.DirectionalLight(0xffffff, 1.6);
  private plates = new THREE.Group(); private horizon = new THREE.Group(); private countryGroup = new THREE.Group(); private labels = new THREE.Group();
  private plateGeometry = new THREE.BoxGeometry(1, 1, 1).translate(0, -.5, 0);
  private plateMaterial = new THREE.MeshStandardMaterial({ roughness: .95, metalness: 0 });
  private outlineMaterial = new THREE.LineBasicMaterial({ transparent: true, opacity: .9 });
  private horizonMaterial = new THREE.LineBasicMaterial({ transparent: true, opacity: .55, depthWrite: false });
  private coastMaterial = new THREE.LineBasicMaterial({ transparent: true, opacity: .5, depthWrite: false });
  private faintMaterial = new THREE.LineBasicMaterial({ transparent: true, opacity: .22, depthWrite: false });
  private bodyMaterial = new THREE.MeshStandardMaterial({ roughness: .72, metalness: .06 });
  private ghostMaterial = new THREE.MeshBasicMaterial({ wireframe: true, transparent: true, opacity: .75 });
  private ringMaterial = new THREE.MeshBasicMaterial({ transparent: true, opacity: .95, depthWrite: false });
  private remoteMaterial = new THREE.MeshBasicMaterial();
  private layers: Record<ShapeKey, InstancedLayer>;
  private rings: InstancedLayer; private remotes: InstancedLayer;
  private links: LineSegments2; private linkMaterial = new LineMaterial({ vertexColors: true, linewidth: 1.6, transparent: true, depthWrite: false });
  private particles: THREE.Points<THREE.BufferGeometry, THREE.ShaderMaterial>;

  private topology: Topology | null = null;
  private nodesById = new Map<string, TopologyNode>();
  private linkCurves: { link: TopologyLink; p0: THREE.Vector3; p1: THREE.Vector3; p2: THREE.Vector3 }[] = [];
  private pulsing: { index: number; matrix: THREE.Matrix4 }[] = [];
  private particleLinks = new Int32Array(0);
  private labelObjects = new Map<string, CSS2DObject>();
  private horizonKey = ""; private countryKey = ""; private horizonBuild = 0;
  private selection: string | null = null; private flowSelection: string | null = null; private hovered: string | null = null;
  private inset = 0;
  private labelsEnabled = true; private paused = false; private reducedMotion: MediaQueryList;
  private time = 0; private last = 0; private lastRender = 0; private frame = 0; private dirty = true; private visible = true; private fitted = false;
  private tween: { fromPosition: THREE.Vector3; toPosition: THREE.Vector3; fromTarget: THREE.Vector3; toTarget: THREE.Vector3; start: number; duration: number } | null = null;
  private cleanup: (() => void)[] = [];

  constructor(private container: HTMLElement, private callbacks: SceneCallbacks) {
    this.renderer = new THREE.WebGLRenderer({ antialias: true, powerPreference: "high-performance" });
    this.renderer.setPixelRatio(Math.min(window.devicePixelRatio || 1, 2));
    this.renderer.shadowMap.enabled = true; this.renderer.shadowMap.type = THREE.PCFSoftShadowMap;
    this.renderer.domElement.className = "topology-canvas";
    this.renderer.domElement.tabIndex = 0;
    this.renderer.domElement.setAttribute("aria-label", "3D network map. Drag to orbit, right-drag or arrow keys to pan, scroll to zoom. Select a device for details.");
    this.labelRenderer.domElement.className = "topology-labels";
    container.append(this.renderer.domElement, this.labelRenderer.domElement);
    this.palette = readPalette(container);
    this.reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)");

    this.controls = new OrbitControls(this.camera, this.renderer.domElement);
    this.controls.enableDamping = true; this.controls.dampingFactor = .09;
    this.controls.maxPolarAngle = Math.PI * .47; this.controls.minDistance = 4; this.controls.maxDistance = 600;
    this.controls.screenSpacePanning = false;
    this.controls.listenToKeyEvents(this.renderer.domElement);
    this.controls.addEventListener("change", () => this.invalidate());
    this.controls.addEventListener("start", () => { this.tween = null; });

    this.sun.castShadow = true; this.sun.shadow.mapSize.set(2048, 2048); this.sun.shadow.bias = -.0006; this.sun.shadow.normalBias = .02;
    this.scene.add(this.hemisphere, this.sun, this.sun.target, this.plates, this.horizon, this.countryGroup, this.labels);
    this.floor = new THREE.Mesh(new THREE.PlaneGeometry(1, 1), new THREE.MeshStandardMaterial({ roughness: 1, metalness: 0 }));
    this.floor.rotation.x = -Math.PI / 2; this.floor.position.y = -.01; this.floor.receiveShadow = true;
    this.scene.add(this.floor);

    this.layers = Object.fromEntries((Object.keys(deviceShapes) as ShapeKey[]).map(key => [key, new InstancedLayer(deviceShapes[key].geometry(), key === "observed" ? this.ghostMaterial : this.bodyMaterial, this.scene, key !== "observed")])) as Record<ShapeKey, InstancedLayer>;
    this.rings = new InstancedLayer(new THREE.RingGeometry(.92, 1.1, 48).rotateX(-Math.PI / 2), this.ringMaterial, this.scene, false);
    this.remotes = new InstancedLayer(new THREE.SphereGeometry(.3, 16, 12), this.remoteMaterial, this.scene, false);
    this.links = new LineSegments2(new LineSegmentsGeometry(), this.linkMaterial);
    this.links.frustumCulled = false; this.links.renderOrder = 1;
    this.scene.add(this.links);
    const particleGeometry = new THREE.BufferGeometry();
    this.particles = new THREE.Points(particleGeometry, new THREE.ShaderMaterial({ vertexShader: particleVertex, fragmentShader: particleFragment, uniforms: { uTime: { value: 0 }, uScale: { value: 1 } }, transparent: true, depthWrite: false }));
    this.particles.frustumCulled = false; this.particles.renderOrder = 2;
    this.scene.add(this.particles);

    const canvas = this.renderer.domElement;
    const move = (event: PointerEvent) => { this.setPointer(event); this.pointerInside = true; this.schedulePick(); };
    const leave = () => { this.pointerInside = false; this.setHovered(null); };
    const down = (event: PointerEvent) => { this.pointerDown = { x: event.clientX, y: event.clientY }; };
    const up = (event: PointerEvent) => {
      const start = this.pointerDown; this.pointerDown = null;
      if (!start || event.button !== 0 || Math.hypot(event.clientX - start.x, event.clientY - start.y) > 5) return;
      this.setPointer(event); this.callbacks.onSelect(this.pick());
    };
    const lost = (event: Event) => { event.preventDefault(); this.callbacks.onContextLost?.(); };
    canvas.addEventListener("pointermove", move); canvas.addEventListener("pointerleave", leave);
    canvas.addEventListener("pointerdown", down); canvas.addEventListener("pointerup", up);
    canvas.addEventListener("webglcontextlost", lost);
    const resize = new ResizeObserver(() => this.resize()); resize.observe(container);
    const intersection = new IntersectionObserver(([entry]) => { this.visible = entry.isIntersecting; this.kick(); }); intersection.observe(container);
    const visibility = () => this.kick(); document.addEventListener("visibilitychange", visibility);
    const motion = () => this.invalidate(); this.reducedMotion.addEventListener("change", motion);
    const theme = () => this.refreshTheme();
    const themeObserver = new MutationObserver(theme); themeObserver.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme", "class", "style"] });
    const scheme = window.matchMedia("(prefers-color-scheme: dark)"); scheme.addEventListener("change", theme);
    this.cleanup.push(() => {
      canvas.removeEventListener("pointermove", move); canvas.removeEventListener("pointerleave", leave);
      canvas.removeEventListener("pointerdown", down); canvas.removeEventListener("pointerup", up);
      canvas.removeEventListener("webglcontextlost", lost);
      resize.disconnect(); intersection.disconnect(); themeObserver.disconnect();
      document.removeEventListener("visibilitychange", visibility);
      this.reducedMotion.removeEventListener("change", motion); scheme.removeEventListener("change", theme);
    });
    this.applyPalette();
    this.resize();
  }

  setTopology(topology: Topology) {
    this.topology = topology;
    this.nodesById = new Map(topology.nodes.map(n => [n.id, n]));
    if (this.selection && !this.nodesById.has(this.selection)) this.selection = null;
    if (this.hovered && !this.nodesById.has(this.hovered)) this.hovered = null;
    this.buildEnvironment();
    this.buildPlates();
    this.buildCurves();
    this.writeNodes();
    this.writeLinks();
    this.writeParticles();
    this.writeLabels();
    if (!this.fitted && topology.nodes.length) { this.fitted = true; this.fit(!this.motionReduced(), true); }
    else if (!this.fitted) this.fit(false);
    this.invalidate();
  }
  setSelection(nodeId: string | null, flowId: string | null) {
    if (nodeId === this.selection && flowId === this.flowSelection) return;
    const focusChanged = nodeId !== this.selection;
    this.selection = nodeId; this.flowSelection = flowId;
    this.writeEmphasis();
    if (focusChanged && nodeId) this.focus(nodeId);
  }
  // Shifts the projection centre right so a left-hand panel does not cover the focused node.
  setInset(left: number) { if (left !== this.inset) { this.inset = left; this.resize(); } }
  setPaused(paused: boolean) { this.paused = paused; this.invalidate(); }
  setLabels(enabled: boolean) { this.labelsEnabled = enabled; this.writeLabels(); this.invalidate(); }

  fit(animate = true, intro = false) {
    const t = this.topology;
    // Frame the local network first; the horizon may run past the viewport edges.
    const height = t?.horizon.height ?? 12, extent = Math.min(t?.extent ?? 30, Math.max((t?.horizon.radius ?? 18) * .85, (t?.lanRadius ?? 8) * 1.45));
    const target = new THREE.Vector3(0, height * .3, 0);
    const halfFov = THREE.MathUtils.degToRad(this.camera.fov / 2);
    const fitHeight = extent / Math.tan(halfFov), fitWidth = extent / (Math.tan(halfFov) * Math.max(this.camera.aspect, .2));
    const distance = Math.max(fitHeight, fitWidth) * .95;
    const direction = new THREE.Vector3(.38, .72, 1).normalize();
    const position = target.clone().addScaledVector(direction, distance);
    this.controls.maxDistance = Math.max(distance, (t?.extent ?? 30) * 3) * 2;
    if (intro) this.moveCamera(target.clone().addScaledVector(direction.clone().setY(1.6).normalize(), distance * 1.35), target, 0);
    this.moveCamera(position, target, animate ? (intro ? 1600 : 750) : 0);
  }
  focus(nodeId: string) {
    const node = this.nodesById.get(nodeId);
    if (!node) return;
    // Aim slightly above the node so its uplinks toward the horizon stay in view.
    const t = this.topology!, target = this.worldPosition(node);
    if (node.kind !== "remote") target.y += t.horizon.height * .22;
    const offset = this.camera.position.clone().sub(this.controls.target);
    if (offset.lengthSq() < 1e-6) offset.set(0, 1, 1);
    offset.normalize();
    offset.y = Math.max(offset.y, .5); offset.normalize();
    this.moveCamera(target.clone().addScaledVector(offset, Math.max(30, t.lanRadius * 1.6)), target, this.motionReduced() ? 0 : 750);
  }
  refreshTheme() {
    const palette = readPalette(this.container);
    if (signature(palette) === signature(this.palette)) return;
    this.palette = palette; this.applyPalette(); this.buildEnvironment(true); this.writeNodes(); this.writeLinks(); this.writeParticles(); this.invalidate();
  }

  dispose() {
    cancelAnimationFrame(this.frame); this.frame = 0;
    this.cleanup.forEach(fn => fn());
    this.controls.dispose();
    Object.values(this.layers).forEach(layer => layer.dispose()); this.rings.dispose(); this.remotes.dispose();
    this.links.geometry.dispose(); this.linkMaterial.dispose();
    this.particles.geometry.dispose(); this.particles.material.dispose();
    this.clearGroup(this.plates); this.clearGroup(this.horizon); this.clearGroup(this.countryGroup);
    this.labelObjects.forEach(label => label.element.remove()); this.labelObjects.clear();
    if (this.grid) { this.grid.geometry.dispose(); (this.grid.material as THREE.Material).dispose(); }
    this.floor.geometry.dispose(); this.floor.material.dispose(); this.plateGeometry.dispose();
    [this.plateMaterial, this.outlineMaterial, this.horizonMaterial, this.coastMaterial, this.faintMaterial, this.bodyMaterial, this.ghostMaterial, this.ringMaterial, this.remoteMaterial].forEach(m => m.dispose());
    this.renderer.dispose(); this.renderer.forceContextLoss();
    this.renderer.domElement.remove(); this.labelRenderer.domElement.remove();
  }

  // --- Rendering loop: runs only while something changes, animates, or the camera settles.
  private invalidate() { this.dirty = true; this.kick(); }
  private kick() { if (!this.frame && this.visible && !document.hidden) this.frame = requestAnimationFrame(this.tick); }
  private motionReduced() { return this.reducedMotion.matches; }
  private ambientMotion() { return !this.paused && !this.motionReduced() && (this.linkCurves.length > 0 || this.pulsing.length > 0); }
  private tick = (now: number) => {
    this.frame = 0;
    if (!this.visible || document.hidden) { this.last = 0; return; }
    const dt = this.last ? Math.min((now - this.last) / 1000, .1) : 0; this.last = now;
    let moving = this.controls.update();
    if (this.tween) {
      const progress = Math.min(1, (now - this.tween.start) / this.tween.duration), eased = progress < .5 ? 4 * progress ** 3 : 1 - (-2 * progress + 2) ** 3 / 2;
      this.camera.position.lerpVectors(this.tween.fromPosition, this.tween.toPosition, eased);
      this.controls.target.lerpVectors(this.tween.fromTarget, this.tween.toTarget, eased);
      this.camera.lookAt(this.controls.target);
      if (progress >= 1) { this.tween = null; this.controls.update(); }
      moving = true;
    }
    if (this.pickPending) { this.pickPending = false; if (this.pointerInside) this.setHovered(this.pick()); }
    const ambient = this.ambientMotion();
    // Ambient-only animation runs at ~30 fps; interaction renders every frame.
    if (this.dirty || moving || (ambient && now - this.lastRender >= 32)) {
      if (ambient) { this.time += dt; this.particles.material.uniforms.uTime.value = this.time; this.animatePulses(); }
      this.renderer.render(this.scene, this.camera);
      this.labelRenderer.render(this.scene, this.camera);
      if (this.dirty || moving) this.declutter();
      this.dirty = false; this.lastRender = now;
    }
    if (moving || ambient || this.tween) this.kick(); else this.last = 0;
  };
  // Hide lower-priority labels that would overlap on screen: selected > hovered > neighbors > plates > infrastructure > busiest > countries.
  private declutter() {
    const width = this.container.clientWidth, height = this.container.clientHeight, point = new THREE.Vector3(), placed: number[][] = [];
    const focus = this.emphasis();
    const rank = (id: string) => { const node = this.nodesById.get(id); return id === this.selection ? 0 : id === this.hovered ? 1 : focus.nodes.has(id) ? 2 : node && node.kind !== "remote" ? 3 + roleOrder.indexOf(node.role) : 9; };
    const entries = [...this.labelObjects].filter(([, label]) => label.visible).map(([id, label]) => ({ label, rank: rank(id), traffic: this.nodesById.get(id)?.bytesPerSecond ?? 0 }))
      .concat([...this.plates.children, ...this.countryGroup.children].filter((o): o is CSS2DObject => o instanceof CSS2DObject).map(label => ({ label, rank: label.element.dataset.kind === "plate" ? 2.5 : 10, traffic: 0 })))
      .sort((a, b) => a.rank - b.rank || b.traffic - a.traffic);
    for (const { label } of entries) {
      const element = label.element;
      if (!label.userData.size?.[0]) label.userData.size = [element.offsetWidth, element.offsetHeight];
      const [w, h] = label.userData.size as number[];
      point.copy(label.position).project(this.camera);
      const x = (point.x + 1) / 2 * width, y = (1 - point.y) / 2 * height, { x: cx, y: cy } = label.center;
      const box = [x - w * cx - 2, y - h * cy - 2, x + w * (1 - cx) + 2, y + h * (1 - cy) + 2];
      const free = point.z < 1 && !placed.some(p => box[0] < p[2] && box[2] > p[0] && box[1] < p[3] && box[3] > p[1]);
      element.style.visibility = free ? "" : "hidden";
      if (free) placed.push(box);
    }
  }
  private moveCamera(position: THREE.Vector3, target: THREE.Vector3, duration: number) {
    if (duration <= 0) { this.tween = null; this.camera.position.copy(position); this.controls.target.copy(target); this.camera.lookAt(target); this.controls.update(); this.invalidate(); return; }
    this.tween = { fromPosition: this.camera.position.clone(), toPosition: position, fromTarget: this.controls.target.clone(), toTarget: target, start: performance.now(), duration };
    this.kick();
  }
  private resize() {
    const width = Math.max(1, this.container.clientWidth), height = Math.max(1, this.container.clientHeight);
    this.renderer.setSize(width, height, false);
    this.labelRenderer.setSize(width, height);
    this.camera.aspect = width / height;
    if (this.inset > 0 && width > this.inset * 2) this.camera.setViewOffset(width, height, -this.inset / 2, 0, width, height); else this.camera.clearViewOffset();
    this.camera.updateProjectionMatrix();
    this.linkMaterial.resolution.set(width, height);
    this.particles.material.uniforms.uScale.value = height * this.renderer.getPixelRatio() / (2 * Math.tan(THREE.MathUtils.degToRad(this.camera.fov / 2)));
    this.invalidate();
  }

  // --- Picking
  private setPointer(event: PointerEvent) {
    const rect = this.renderer.domElement.getBoundingClientRect();
    this.pointer.set((event.clientX - rect.left) / rect.width * 2 - 1, -(event.clientY - rect.top) / rect.height * 2 + 1);
  }
  private schedulePick() { this.pickPending = true; this.kick(); }
  private pick(): string | null {
    this.raycaster.setFromCamera(this.pointer, this.camera);
    const candidates = [...Object.values(this.layers), this.remotes].filter(layer => layer.mesh.count > 0);
    const hit = this.raycaster.intersectObjects(candidates.map(layer => layer.mesh), false)[0];
    if (hit && hit.instanceId !== undefined) return candidates.find(layer => layer.mesh === hit.object)?.ids[hit.instanceId] ?? null;
    // Small or distant nodes are hard to hit exactly; fall back to the nearest node on screen.
    const width = this.container.clientWidth, height = this.container.clientHeight, point = new THREE.Vector3();
    let nearest: string | null = null, best = 16;
    for (const node of this.nodesById.values()) {
      point.copy(this.worldPosition(node)).project(this.camera);
      if (point.z > 1) continue;
      const distance = Math.hypot((point.x - this.pointer.x) / 2 * width, (point.y - this.pointer.y) / 2 * height);
      if (distance < best) { best = distance; nearest = node.id; }
    }
    return nearest;
  }
  private setHovered(nodeId: string | null) {
    if (nodeId === this.hovered) return;
    this.hovered = nodeId;
    this.renderer.domElement.style.cursor = nodeId ? "pointer" : "";
    this.callbacks.onHover?.(nodeId);
    this.writeEmphasis();
  }

  // --- Scene construction
  private worldPosition(node: TopologyNode, top = false) {
    const [x, y, z] = node.position;
    if (node.kind === "remote") return new THREE.Vector3(x, y, z);
    return new THREE.Vector3(x, PLATE_HEIGHT + y + (top ? deviceShapes[this.shapeKey(node)].height * DEVICE_SCALE : 0), z);
  }
  private shapeKey(node: TopologyNode): ShapeKey { return node.kind === "observed" ? "observed" : node.role; }
  private emphasis() { return this.topology ? neighborhood(this.topology, this.selection ?? (this.flowSelection ? null : this.hovered), this.flowSelection) : { nodes: new Set<string>(), links: new Set<string>(), active: false }; }

  private applyPalette() {
    const p = this.palette;
    this.renderer.setClearColor(p.bg);
    this.scene.fog = new THREE.Fog(p.bg, (this.topology?.extent ?? 30) * 2.2, (this.topology?.extent ?? 30) * 5.5);
    this.floor.material.color.copy(p.floor);
    this.plateMaterial.color.copy(p.plate);
    this.outlineMaterial.color.copy(p.borderStrong);
    this.horizonMaterial.color.copy(p.borderStrong); this.faintMaterial.color.copy(p.borderStrong);
    this.coastMaterial.color.copy(p.dark ? p.muted : p.borderStrong); this.coastMaterial.opacity = p.dark ? .32 : .5;
    this.ghostMaterial.color.copy(p.muted);
    this.hemisphere.color.set(0xffffff); this.hemisphere.groundColor.copy(p.bg);
    this.hemisphere.intensity = p.dark ? .9 : 1.25; this.sun.intensity = p.dark ? 1.3 : 1.7;
    this.particles.material.blending = p.dark ? THREE.AdditiveBlending : THREE.NormalBlending;
    this.particles.material.needsUpdate = true;
    this.container.dataset.scheme = p.dark ? "dark" : "light";
  }
  private clearGroup(group: THREE.Group) {
    for (const child of [...group.children]) {
      group.remove(child);
      child.traverse(object => { if (object instanceof THREE.Mesh || object instanceof THREE.Line) { if (object.geometry !== this.plateGeometry) object.geometry.dispose(); } if (object instanceof CSS2DObject) object.element.remove(); });
    }
  }
  private label(text: string, kind: string, detail?: string) {
    const element = document.createElement("div");
    element.className = "topology-label"; element.dataset.kind = kind;
    const title = document.createElement("span"); title.textContent = text; element.append(title);
    if (detail) { const small = document.createElement("small"); small.textContent = detail; element.append(small); }
    return new CSS2DObject(element);
  }
  private buildEnvironment(force = false) {
    const t = this.topology; if (!t) return;
    const { radius, height, band, unlocated } = t.horizon, extent = t.extent;
    const key = `${extent.toFixed(2)}|${height.toFixed(2)}|${t.horizon.referenceLongitude}|${this.palette.dark}`;
    if (key !== this.horizonKey || force) {
      this.horizonKey = key;
      this.floor.scale.set(extent * 12, extent * 12, 1);
      this.scene.fog = new THREE.Fog(this.palette.bg, extent * 2.2, extent * 5.5);
      if (this.grid) { this.scene.remove(this.grid); this.grid.geometry.dispose(); (this.grid.material as THREE.Material).dispose(); }
      const size = Math.ceil(extent * 2.6 / 4) * 4;
      this.grid = new THREE.GridHelper(size, size / 2, this.palette.borderStrong, this.palette.border);
      Object.assign(this.grid.material as THREE.Material, { transparent: true, opacity: this.palette.dark ? .5 : .75 });
      this.scene.add(this.grid);
      const light = extent * 1.4;
      this.sun.position.set(-light * .55, light, light * .75);
      Object.assign(this.sun.shadow.camera, { left: -extent, right: extent, top: extent, bottom: -extent, near: 1, far: light * 3 });
      this.sun.shadow.camera.updateProjectionMatrix();

      this.clearGroup(this.horizon);
      const build = ++this.horizonBuild;
      this.horizon.add(this.ring(height - band, radius, this.horizonMaterial), this.ring(height + band, radius, this.horizonMaterial), this.ring(height, radius, this.faintMaterial));
      const ticks: THREE.Vector3[] = [];
      for (let lon = -180; lon < 180; lon += 30) {
        ticks.push(new THREE.Vector3(...cylinderPoint(-90, lon, t.horizon)), new THREE.Vector3(...cylinderPoint(90, lon, t.horizon)));
        if (lon % 90) continue;
        const axis = this.label(lon === 0 ? "0°" : lon === -180 ? "180°" : `${Math.abs(lon)}°${lon > 0 ? "E" : "W"}`, "axis");
        axis.position.set(...cylinderPoint(-90, lon, t.horizon)); axis.position.y -= .8;
        this.horizon.add(axis);
      }
      this.horizon.add(new THREE.LineSegments(new THREE.BufferGeometry().setFromPoints(ticks), this.faintMaterial));
      loadCoastlines().then(lines => {
        if (build !== this.horizonBuild) return;
        const points: number[] = [];
        for (const line of lines) for (let i = 1; i < line.length; i++) {
          const [lon0, lat0] = line[i - 1], [lon1, lat1] = line[i];
          if (Math.abs(lon1 - lon0) > 180) continue;
          points.push(...cylinderPoint(lat0, lon0, t.horizon), ...cylinderPoint(lat1, lon1, t.horizon));
        }
        const geometry = new THREE.BufferGeometry(); geometry.setAttribute("position", new THREE.Float32BufferAttribute(points, 3));
        this.horizon.add(new THREE.LineSegments(geometry, this.coastMaterial)); this.invalidate();
      }).catch(() => { /* The horizon still works without coastlines. */ });
    }
    const hasUnlocated = t.nodes.some(n => n.kind === "remote" && !n.located);
    const countryKey = `${key}|${t.countries.slice(0, 6).map(c => `${c.code}:${c.position.map(v => v.toFixed(1))}`).join()}|${hasUnlocated}`;
    if (countryKey === this.countryKey && !force) return;
    this.countryKey = countryKey;
    this.clearGroup(this.countryGroup);
    for (const country of t.countries.slice(0, 6)) {
      const label = this.label(country.name, "country"); label.position.set(...country.position); this.countryGroup.add(label);
    }
    if (hasUnlocated) {
      const label = this.label("Location unknown", "country"); label.position.set(4.6, unlocated[1], 0);
      this.countryGroup.add(this.ring(unlocated[1], 4.2, this.faintMaterial), label);
    }
    this.invalidate();
  }
  private ring(y: number, radius: number, material: THREE.LineBasicMaterial) {
    const points = Array.from({ length: 129 }, (_, i) => new THREE.Vector3(Math.sin(i / 128 * Math.PI * 2) * radius, y, Math.cos(i / 128 * Math.PI * 2) * radius));
    return new THREE.Line(new THREE.BufferGeometry().setFromPoints(points), material);
  }
  private buildPlates() {
    this.clearGroup(this.plates);
    for (const group of this.topology?.groups ?? []) {
      const [w, d] = group.size, [x, z] = group.center;
      const plate = new THREE.Mesh(this.plateGeometry, this.plateMaterial);
      plate.scale.set(w, PLATE_HEIGHT, d); plate.position.set(x, PLATE_HEIGHT, z); plate.receiveShadow = true; plate.castShadow = true;
      const corners = [[-w / 2, -d / 2], [w / 2, -d / 2], [w / 2, d / 2], [-w / 2, d / 2], [-w / 2, -d / 2]].map(([cx, cz]) => new THREE.Vector3(x + cx, PLATE_HEIGHT + .005, z + cz));
      const outline = new THREE.Line(new THREE.BufferGeometry().setFromPoints(corners), this.outlineMaterial);
      const label = this.label(group.label, "plate", `${group.count} ${group.count === 1 ? "host" : "hosts"}`);
      label.position.set(x - w / 2, PLATE_HEIGHT, z + d / 2 + .9); label.center.set(0, 0);
      this.plates.add(plate, outline, label);
    }
  }
  private buildCurves() {
    const t = this.topology; this.linkCurves = [];
    if (!t) return;
    for (const link of t.links) {
      const source = this.nodesById.get(link.source), target = this.nodesById.get(link.target);
      if (!source || !target) continue;
      const a = this.worldPosition(source, true), b = this.worldPosition(target);
      // The cable rises from the device before bending toward the horizon, so bundles read as uplinks.
      const control = new THREE.Vector3(a.x + (b.x - a.x) * .18, b.y * .92 + 1, a.z + (b.z - a.z) * .18);
      const [p0, p2] = link.direction === "inbound" ? [b, a] : [a, b];
      this.linkCurves.push({ link, p0, p1: control, p2 });
    }
  }
  private writeNodes() {
    const t = this.topology; if (!t) return;
    const p = this.palette, focus = this.emphasis();
    const byLayer = new Map<ShapeKey, TopologyNode[]>(), remotes: TopologyNode[] = [], locals: TopologyNode[] = [];
    for (const node of t.nodes) {
      if (node.kind === "remote") { remotes.push(node); continue; }
      locals.push(node);
      const key = this.shapeKey(node); byLayer.set(key, [...(byLayer.get(key) ?? []), node]);
    }
    const matrix = new THREE.Matrix4(), color = new THREE.Color(), scale = new THREE.Vector3(), rotation = new THREE.Quaternion();
    for (const key of Object.keys(this.layers) as ShapeKey[]) {
      const layer = this.layers[key], nodes = byLayer.get(key) ?? [];
      layer.resize(nodes.length); layer.ids = nodes.map(n => n.id);
      nodes.forEach((node, i) => {
        const position = this.worldPosition(node);
        const selected = node.id === this.selection, hovered = node.id === this.hovered;
        matrix.compose(position, rotation, scale.setScalar(DEVICE_SCALE * (selected ? 1.12 : 1)));
        layer.mesh.setMatrixAt(i, matrix);
        color.copy(node.kind === "observed" ? p.muted : p.body);
        if (inactive(node.status)) color.lerp(p.bg, .55);
        if (selected) color.lerp(p.accent, .6); else if (hovered) color.lerp(p.accent, .3);
        if (focus.active && !focus.nodes.has(node.id)) color.lerp(p.bg, .6);
        layer.mesh.setColorAt(i, color);
      });
      layer.commit();
    }
    this.rings.resize(locals.length); this.rings.ids = locals.map(n => n.id); this.pulsing = [];
    locals.forEach((node, i) => {
      const position = this.worldPosition(node); position.y += .012;
      matrix.compose(position, rotation, scale.setScalar(DEVICE_SCALE * deviceShapes[this.shapeKey(node)].footprint * (node.id === this.selection ? 1.25 : 1)));
      this.rings.mesh.setMatrixAt(i, matrix);
      color.copy(node.id === this.selection ? p.accent : statusColor(p, node.status));
      if (focus.active && !focus.nodes.has(node.id)) color.lerp(p.bg, .6);
      this.rings.mesh.setColorAt(i, color);
      if (node.status === "warning" || node.status === "starting") this.pulsing.push({ index: i, matrix: matrix.clone() });
    });
    this.rings.commit();
    this.remotes.resize(remotes.length); this.remotes.ids = remotes.map(n => n.id);
    const outbound = new Map<string, number>();
    for (const link of t.links) outbound.set(link.target, (outbound.get(link.target) ?? 0) + (link.direction === "outbound" ? link.bytesPerSecond : -link.bytesPerSecond));
    remotes.forEach((node, i) => {
      const size = .7 + intensity(node.bytesPerSecond) * 1.1;
      matrix.compose(this.worldPosition(node), rotation, scale.setScalar(size * (node.id === this.selection ? 1.4 : node.id === this.hovered ? 1.2 : 1)));
      this.remotes.mesh.setMatrixAt(i, matrix);
      color.copy((outbound.get(node.id) ?? 0) >= 0 ? p.accent : p.info);
      if (node.id === this.selection) color.lerp(p.text, .25);
      if (focus.active && !focus.nodes.has(node.id)) color.lerp(p.bg, .75);
      this.remotes.mesh.setColorAt(i, color);
    });
    this.remotes.commit();
  }
  private animatePulses() {
    if (!this.pulsing.length) return;
    const factor = 1 + Math.sin(this.time * 3.2) * .14, matrix = new THREE.Matrix4(), scale = new THREE.Matrix4().makeScale(factor, 1, factor);
    for (const { index, matrix: base } of this.pulsing) { matrix.copy(base).multiply(scale); this.rings.mesh.setMatrixAt(index, matrix); }
    this.rings.mesh.instanceMatrix.needsUpdate = true;
  }
  private writeLinks() {
    const focus = this.emphasis(), p = this.palette, point = new THREE.Vector3(), previous = new THREE.Vector3(), color = new THREE.Color();
    const positions = new Float32Array(this.linkCurves.length * CURVE_SEGMENTS * 6), colors = new Float32Array(positions.length);
    // Dense bundles fade so hundreds of flows stay readable; selection restores full strength.
    const density = Math.min(1, Math.sqrt(80 / Math.max(1, this.linkCurves.length)));
    let offset = 0;
    for (const { link, p0, p1, p2 } of this.linkCurves) {
      const level = intensity(link.bytesPerSecond);
      color.copy(p.bg).lerp(link.direction === "outbound" ? p.accent : p.info, focus.active ? (focus.links.has(link.id) ? 1 : .08) : (.25 + level * .65) * (.45 + .55 * density));
      previous.copy(p0);
      for (let s = 1; s <= CURVE_SEGMENTS; s++) {
        const t = s / CURVE_SEGMENTS, u = 1 - t;
        point.set(0, 0, 0).addScaledVector(p0, u * u).addScaledVector(p1, 2 * u * t).addScaledVector(p2, t * t);
        positions.set([previous.x, previous.y, previous.z, point.x, point.y, point.z], offset);
        colors.set([color.r, color.g, color.b, color.r, color.g, color.b], offset);
        previous.copy(point); offset += 6;
      }
    }
    const geometry = new LineSegmentsGeometry();
    if (positions.length) { geometry.setPositions(positions); geometry.setColors(colors); }
    this.links.geometry.dispose(); this.links.geometry = geometry;
    this.links.visible = positions.length > 0;
    this.linkMaterial.linewidth = focus.active ? 2 : 1.6;
  }
  private writeParticles() {
    const focus = this.emphasis(), p = this.palette;
    const plan = this.linkCurves.map(({ link }) => Math.max(1, Math.min(8, Math.round(Math.log10(link.bytesPerSecond + 1) * 1.2 - 1))));
    const planned = plan.reduce((sum, n) => sum + n, 0), budget = planned > MAX_PARTICLES ? MAX_PARTICLES / planned : 1;
    const counts = plan.map(n => Math.max(1, Math.floor(n * budget)));
    const total = counts.reduce((sum, n) => sum + n, 0);
    const p0 = new Float32Array(total * 3), p1 = new Float32Array(total * 3), p2 = new Float32Array(total * 3), params = new Float32Array(total * 4), colors = new Float32Array(total * 3);
    let index = 0;
    this.particleLinks = new Int32Array(total);
    this.linkCurves.forEach(({ link, p0: a, p1: b, p2: c }, i) => {
      const level = intensity(link.bytesPerSecond), length = a.distanceTo(b) + b.distanceTo(c);
      const speed = (4 + level * 5) / Math.max(length, 1), phase = hash(link.id);
      const color = (link.direction === "outbound" ? p.accent : p.info).clone();
      if (p.dark) color.lerp(p.text, .15);
      const emphasis = !focus.active ? 1 : focus.links.has(link.id) ? 1 : .08;
      for (let k = 0; k < counts[i]; k++, index++) {
        this.particleLinks[index] = i;
        p0.set([a.x, a.y, a.z], index * 3); p1.set([b.x, b.y, b.z], index * 3); p2.set([c.x, c.y, c.z], index * 3);
        params.set([(k / counts[i] + phase) % 1, speed, .32 + level * .34, emphasis], index * 4);
        colors.set([color.r, color.g, color.b], index * 3);
      }
    });
    const geometry = new THREE.BufferGeometry();
    geometry.setAttribute("position", new THREE.BufferAttribute(new Float32Array(total * 3), 3));
    geometry.setAttribute("aP0", new THREE.BufferAttribute(p0, 3)); geometry.setAttribute("aP1", new THREE.BufferAttribute(p1, 3)); geometry.setAttribute("aP2", new THREE.BufferAttribute(p2, 3));
    geometry.setAttribute("aParams", new THREE.BufferAttribute(params, 4)); geometry.setAttribute("aColor", new THREE.BufferAttribute(colors, 3));
    this.particles.geometry.dispose(); this.particles.geometry = geometry;
  }
  private writeEmphasis() {
    this.writeNodes(); this.writeLinks(); this.writeLabels();
    // Particle paths are unchanged by hover/selection; only their emphasis channel is rewritten.
    const attribute = this.particles.geometry.getAttribute("aParams") as THREE.BufferAttribute | undefined;
    if (attribute) {
      const focus = this.emphasis();
      for (let i = 0; i < this.particleLinks.length; i++) attribute.setW(i, !focus.active || focus.links.has(this.linkCurves[this.particleLinks[i]].link.id) ? 1 : .08);
      attribute.needsUpdate = true;
    }
    this.invalidate();
  }
  private writeLabels() {
    const t = this.topology; if (!t) return;
    const focus = this.emphasis();
    const locals = t.nodes.filter(n => n.kind !== "remote"), crowded = locals.length > 24;
    const wanted = new Set<string>();
    for (const node of t.nodes) {
      const emphasized = node.id === this.selection || node.id === this.hovered || (focus.active && focus.nodes.has(node.id) && focus.nodes.size <= 30);
      const always = this.labelsEnabled && node.kind !== "remote" && (!crowded || node.role === "gateway" || node.role === "switch" || node.role === "server");
      if (emphasized || always) wanted.add(node.id);
    }
    for (const [id, label] of this.labelObjects) if (!this.nodesById.has(id)) { this.labels.remove(label); label.element.remove(); this.labelObjects.delete(id); }
    for (const node of t.nodes) {
      let label = this.labelObjects.get(node.id);
      if (!wanted.has(node.id)) { if (label) label.visible = false; continue; }
      const detailed = node.id === this.selection || node.id === this.hovered;
      const detail = !detailed ? undefined : node.kind === "remote" ? [node.city, node.countryName].filter(Boolean).join(", ") || "Location unknown" : node.kind === "observed" ? "Not in inventory" : node.ip;
      if (!label || label.userData.text !== `${node.label}|${detail ?? ""}`) {
        if (label) { this.labels.remove(label); label.element.remove(); }
        label = this.label(node.label, node.kind, detail); label.userData.text = `${node.label}|${detail ?? ""}`; label.center.set(.5, 1);
        this.labelObjects.set(node.id, label); this.labels.add(label);
      }
      label.position.copy(this.worldPosition(node, true)); label.position.y += node.kind === "remote" ? 1.1 : .35;
      label.element.dataset.state = node.id === this.selection ? "selected" : node.id === this.hovered ? "hover" : focus.active && !focus.nodes.has(node.id) ? "dim" : "";
      label.visible = true;
    }
  }
}
