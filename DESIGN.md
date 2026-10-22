# NERVE design

How the web UI looks and why. The tokens live in `app/globals.css`; this file explains them.
When the two disagree, fix one of them in the same change.

## Character

A precise instrument for network operators: calm, dark, dense and honest. The interface must
never make a guess look like a fact.

## Principles

1. **Attention first.** A screen opens with what needs the operator, then what changed.
2. **Provenance is visible.** Every derived item names its source (discovery, reverse DNS, an
   agent, an operator). Missing data is said in words, never filled in.
3. **Colour means state.** Green, amber, red and grey carry status only. Actions, navigation,
   selection and charts that are not states stay neutral.
4. **Density over air.** Controls are 30–32 px high; rows separate with 1 px dividers instead of
   cards. Panels are for real objects, not for every group.
5. **No decoration.** No glows, glass, gradients, coloured left borders or uppercase labels.

## Colour tokens

Dark is the primary design; the light theme uses the same token names.

| Token | Dark | Light | Use |
|---|---|---|---|
| `--bg` | `#0e1110` | `#f3f5f4` | Page |
| `--surface` | `#151918` | `#ffffff` | Panels, top bar, overview rail |
| `--surface-raised` | `#1c211f` | `#ffffff` | Inputs, menus |
| `--border` / `--border-strong` | `#262c2a` / `#343c39` | `#dde2e0` / `#c3cbc7` | Dividers, control outlines |
| `--text` | `#e4e8e6` | `#161b19` | Primary text |
| `--text-secondary` / `--text-muted` | `#b6bfbb` / `#9aa4a0` | `#424b47` / `#5f6964` | Secondary text, labels |
| `--accent` / `--on-accent` | `#e4e8e6` / `#0e1110` | `#161b19` / `#ffffff` | Primary action (neutral) |
| `--success` | `#6fbf9c` | `#1f7a52` | Online, healthy |
| `--warning` | `#d9a441` | `#8f5f06` | Needs attention, degraded |
| `--danger` | `#e06c64` | `#b8372f` | Problem: unexpected offline, failure |
| `--unknown` | `#7c8783` | `#6b7570` | Offline (known), no data |
| `--operator` | `#a9b4c9` | `#4d5a73` | Set by an operator |
| `--info` | `#7fa7c9` | `#2c6890` | Inbound traffic, starting |
| `--focus` | `#8fd0b4` | `#1f7a52` | Keyboard focus ring only |

Secondary and muted text keep at least 4.5:1 against `--surface` and `--bg`.

## Status shapes

The shape carries the meaning; colour supports it, so states stay readable without colour.
Use `<span class="status-dot" data-status="…">`.

| `data-status` | Shape | Meaning |
|---|---|---|
| `online` | filled circle | Answered the last check |
| `offline`, `stopped` | ring | Known, currently silent |
| `unknown` | dashed ring | No data |
| `warning` | outlined diamond | Needs review or attention |
| `limited` | filled diamond | Degraded or limited coverage |
| `problem` | filled triangle | Something is wrong |
| `operator` | small square | Set by an operator |
| `info` | grey dot | Informational event |

## Typography

- IBM Plex Sans for the interface, IBM Plex Mono for IP and MAC addresses, ports, keys and
  timestamps. Both load through `next/font` in `app/layout.tsx` and are served from the app's own
  origin, as the proxy's `font-src 'self'` policy requires.
- Base size 13 px. Scale: 11 / 12 / 13 / 15 / 20 / 24. Page titles 20 px semibold, section
  headings 15 px semibold. Nothing functional below 11 px.
- Tabular figures in tables, times and counts. Sentence case everywhere.

## Layout

- **Shell:** one sticky top bar on every screen with the brand, the real sections (Overview,
  Devices, Traffic map, Settings), a device search that opens `/devices?search=…` (`/` focuses
  it), the core state and the theme switch. Planned modules are not listed until they ship.
- **Overview:** three columns — *Needs attention* (derived from discovery state, duplicates and
  device status, each with its source), *Recent activity* (events) and a *Coverage* rail with the
  address map of the configured subnet (IPv4, up to 256 addresses, placed by primary address)
  and the state of the discovery agent, database and core. Columns stack below 1100 px and 760 px.
- **Radii:** 4 px controls and cells, 6 px panels, 8 px dialogs. Shadows only for popovers.

## Motion

150–200 ms for state changes only. No page-load sequences, pulses or ambient animation.
