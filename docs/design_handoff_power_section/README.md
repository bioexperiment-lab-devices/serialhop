# Handoff: SerialHop — Status tab "Power" section

## Overview
A new section on the SerialHop Operator Panel's **Status** tab that exposes
the OS power-keep-awake capability. It surfaces whether the local SerialHop
service is currently preventing Windows from sleeping/hibernating, and lets
the operator toggle that state with a single click.

Lives between the **Service health** group of lamps and the **Service
control** action row.

## About the design files
The files in this bundle are **design references created in HTML** —
prototypes showing the intended look and behaviour. They are not production
code to copy verbatim. The task is to recreate this UI in the SerialHop
panel's actual codebase, using its existing component library, state plumbing,
and IPC channels to the service.

- `full-prototype.html` — the entire Status-tab artboard set, all panel
  states embedded on a design canvas. Open in a browser to interact: each
  artboard is a frozen render of one Power-section state.
- `panel-status.jsx` — the React source for `PowerRow` (the component you're
  implementing) and the `StatusTab` it slots into. The shape of the
  `keepAwake` prop and the visual states are authoritative.
- `panel-shell.jsx` — the shared panel primitives (`Help`, `Lamp`, `Button`,
  etc.) that `PowerRow` borrows from. Use as a style reference for tone &
  density.

## Fidelity
**High-fidelity.** Colours, spacing, type scale, hover/focus/disabled states,
and the disabled-vs-not-allowed semantics are all final. Recreate
pixel-accurately using the codebase's existing primitives where they exist
(buttons, lamps, help popovers); only introduce new styles for what
`PowerRow` itself adds.

## The component: `PowerRow`

### Anatomy

```
┌─────────────────────────────────────────────────────────────────┐
│ KEEP SYSTEM AWAKE  (?)                                          │
│                                                                  │
│  ●  On                                       ┌──────────────┐   │
│     System will not sleep or auto-shutdown.  │Click to disable│ │
│                                              └──────────────┘   │
└─────────────────────────────────────────────────────────────────┘
   ↑                                              ↑
   The entire card is a single <button>.          Pill chip is a
   Clicking anywhere on it toggles keep-awake.    visual affordance,
                                                  not a separate target.
```

Visually it shares the **same card shape, padding, border, and typography
as the lamps in Service health** — it reads as a fourth lamp, but it's
interactive. The pill chip on the right tells the user the card is
clickable and what clicking will do.

### Props

```ts
type KeepAwakeState =
  | { state: 'on'  | 'off';    inFlight?: boolean }
  | { state: 'unreachable' };

interface PowerRowProps {
  keepAwake: KeepAwakeState;
  onToggle: () => void;            // fire when the card is clicked
  openHelpId?: string;             // for the (?) help popover wiring,
                                   // same protocol as other lamps
}
```

### State matrix

| `state`       | `inFlight` | Dot   | Label | Sub-text                                  | Chip text         | Card |
|---------------|------------|-------|-------|-------------------------------------------|-------------------|------|
| `on`          | false      | green | `On`  | `System will not sleep or auto-shutdown.` | `Click to disable`| clickable |
| `on`          | true       | green | `On`  | `System will not sleep or auto-shutdown.` | `Disabling…`      | disabled |
| `off`         | false      | grey  | `Off` | `Click to keep the system awake.`         | `Click to enable` | clickable |
| `off`         | true       | grey  | `Off` | `Click to keep the system awake.`         | `Enabling…`       | disabled |
| `unreachable` | —          | grey  | `—`   | `Service unreachable`                     | *(omit chip)*     | disabled, cursor `not-allowed` |

`unreachable` is shown when the local SerialHop service is not running or
not reachable; the toggle is locked out entirely. No pill chip in that
state — the dimmed card itself communicates "can't do anything here."

### Interaction

- **Click anywhere on the card** → call `onToggle`. The whole card is a
  single `<button>`; the chip is decorative.
- **Help icon** — the `(?)` next to "Keep system awake" must `stopPropagation`
  on its click handler so it doesn't fire the toggle. It opens a popover
  with this copy:
  - Title: **Keep system awake**
  - What: *"Prevents Windows from idling into sleep, hibernate, or scheduled
    automatic shutdown while the SerialHop service is running."*
  - When: *"Has no effect on user-initiated shutdown, restart, or sign-out.
    Cleared if the service stops, crashes, or is updated."*
- **In-flight** — while a toggle RPC is pending, the card is disabled and
  the chip text swaps to `Disabling…` / `Enabling…`. The dot does **not**
  change tone until the service confirms; this avoids flashing a green dot
  during a request that might fail.
- **Failure** — if the RPC fails, drop back to the previous tone, leave the
  card enabled, and surface the error in the footer status strip (existing
  pattern). The Power section itself shows no inline error.
- **Hover** — the chip's text shifts to `var(--accent)` and its
  background/border to `var(--accent-soft)` / `var(--accent-border)`. The
  card itself doesn't change. Transition 120ms ease on all three
  properties.
- **Focus** — keyboard focus on the card → 2px outline in `var(--accent)`
  with 1px offset (matches every other focusable surface in the panel).
- **Disabled** — `cursor: not-allowed`, `opacity: 0.7`, no hover effects.

### Layout & sizing

- Card: same as `.shp-lamp` — 1px border `var(--border)`, 4px radius,
  white surface, 12px 14px padding, 8px internal gap (column).
- Row 1 ("KEEP SYSTEM AWAKE"): 11px uppercase 600 weight with `0.08em`
  letter-spacing, `var(--text-muted)`. Help icon trails by 8px.
- Row 2 (dot · label/sub · chip): 9px gap between dot and text;
  `margin-left: auto` on the chip pushes it to the right edge.
- Dot: 12px ⌀, tone-coloured, with a 3px soft halo
  (`box-shadow: 0 0 0 3px var(--success-soft)` etc.) — identical to other
  lamps.
- Pill chip: 4px 10px padding, `border-radius: 999px`, 11.5px 500-weight
  text. Base tone is `var(--surface)` + `var(--border)`; hover swaps to the
  accent-soft palette.

### Container margin
The section title `Power` is rendered with the existing `.shp-h` style
(uppercase 11px, hairline below). `PowerRow` has `margin-bottom: 18px` to
separate it from the Service control row below.

## Design tokens used

All tokens already exist in the panel's stylesheet — do not introduce new
ones for this section.

| Token                | Value (light)  | Used for                           |
|----------------------|----------------|------------------------------------|
| `--surface`          | `#FFFFFF`      | card + chip base                   |
| `--surface-sunken`   | `#F8F6F0`      | dot halo (grey)                    |
| `--surface-strip`    | `#FAF8F3`      | (card hover would use, but unused) |
| `--border`           | `#E2DED2`      | card border, chip border           |
| `--border-strong`    | `#C8C3B5`      | not used here                      |
| `--text`             | `#1A1916`      | label                              |
| `--text-secondary`   | `#514E47`      | chip text (idle), sub-text         |
| `--text-muted`       | `#8A8678`      | section/header text, dot grey      |
| `--accent`           | `#1F3A8A`      | chip text on hover, focus outline  |
| `--accent-soft`      | `#E7ECF6`      | chip background on hover           |
| `--accent-border`    | `#B8C2DC`      | chip border on hover               |
| `--success`          | `#2F7D3F`      | dot when `state === 'on'`          |
| `--success-soft`     | `#E5F1E6`      | dot halo when `on`                 |

Dark-theme variants exist in `:root[data-theme="dark"]`. The component is
theme-agnostic — it only consumes tokens.

## State management & wiring

The service exposes (or should expose) a single endpoint on its REST
control surface:

- `GET /power/keep-awake` → `{ state: 'on'|'off' }`
- `POST /power/keep-awake` → `{ desired: 'on'|'off' }` → 200 on accept,
  4xx/5xx on refuse.

In the panel:

1. Poll `GET` every 2s on the Status tab (consistent with lamp polling).
2. If the service health probe shows the local service is down, set
   `keepAwake.state = 'unreachable'` directly — don't even attempt the GET.
3. On toggle: set `inFlight: true`, fire `POST`, then re-poll once on
   completion and clear `inFlight`. A 5s ceiling on the in-flight state to
   prevent a stuck spinner.

State is **not** persisted in the panel — the service owns it. Refreshing
the panel UI should not change the actual OS power policy.

## Accessibility

- The card is a `<button type="button">`. Keyboard activatable via Enter
  and Space.
- Visible focus outline (see above).
- The chip is `<span>` — purely decorative.
- The `(?)` help control is a button-like region with `cursor: help`. Its
  popover should be keyboard-reachable in your implementation even if the
  prototype only shows it on click (the prototype demos one open state via
  the `openHelpId` prop).
- `aria-pressed` on the card reflects `state === 'on'` (don't set it when
  `unreachable`).
- `aria-busy="true"` on the card when `inFlight`.
- `aria-disabled` when `unreachable` or `inFlight`.

## Files in this bundle

- `README.md` — this file.
- `full-prototype.html` — full interactive canvas of every Status-tab
  state, including all four Power-section variants:
  - `01 · Running, keep-awake on, update available`
  - `02 · Keep-awake — Off (reachable)`
  - `03 · Keep-awake — enabling… (request in flight)`
  - `04 · Keep-awake — help popover open`
  - `08 · Service not installed (keep-awake unreachable)`
- `panel-status.jsx` — source for `PowerRow` and `StatusTab`.
- `panel-shell.jsx` — shared primitives (`Help`, `Lamp`, `Button`, etc.) the
  Power section visually mirrors.

## Assets
None — the section uses no images or icons beyond the existing in-CSS
dots and the textual `?` for the help affordance.
