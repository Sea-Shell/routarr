# Routarr — Signal Console Design Palette

> Monospace terminal meets modern dark control room.
> Every color is a signal. No decoration.

---

## Colors

| Token | Hex | Usage |
|---|---|---|
| `--bg` | `#07100d` | Deepest dark green-black, page background |
| `--panel-bg` | `#0d1a14` | Card / panel surface |
| `--border` | `#1e3328` | Panel borders, table dividers, section separators |
| `--text` | `#e8f6df` | Body text, headings (h2), buttons |
| `--muted` | `#88a393` | Labels, secondary text, placeholder states |
| `--green` | `#8dff9a` | Primary CTA, brand, success, headings (h1) |
| `--amber` | `#ffc857` | Warnings, pending review, attention |
| `--red` | `#ff6b4a` | Errors, danger, missing tracks |
| `--info` | `#64c8ff` | Info badges (rare) |

### Badge / Flash backgrounds

| Token | Background | Border | Text |
|---|---|---|---|
| `badge-ok` | `rgba(141,255,154,0.10)` | `rgba(141,255,154,0.30)` | `--green` |
| `badge-warn` | `rgba(255,200,87,0.10)` | `rgba(255,200,87,0.30)` | `--amber` |
| `badge-danger` | `rgba(255,107,74,0.10)` | `rgba(255,107,74,0.30)` | `--red` |
| `badge-muted` | `rgba(136,163,147,0.10)` | `rgba(136,163,147,0.30)` | `--muted` |
| `badge-info` | `rgba(100,200,255,0.10)` | `rgba(100,200,255,0.30)` | `--info` |
| `flash` | `rgba(141,255,154,0.07)` | `rgba(141,255,154,0.22)` | `--green` |
| `error` | `rgba(255,107,74,0.07)` | `rgba(255,107,74,0.22)` | `--red` |

### Row highlights

| Token | Value | Usage |
|---|---|---|
| Row tint | `rgba(255,255,255,0.015)` | Hover/active row background |
| Row divider | `rgba(255,255,255,0.04)` | Subtle horizontal rule |

---

## Typography

```
Font: ui-monospace, 'IBM Plex Mono', monospace
Line-height: 1.6
```

| Size | Element |
|---|---|
| `0.70rem` | Table header labels |
| `0.72rem` | Badges, definition labels, timestamp dts |
| `0.78rem` | Form labels, route subtext IDs |
| `0.82rem` | Buttons |
| `0.85rem` | Table cells, nav links |
| `0.90rem` | **Base body** |
| `1.00rem` | Nav brand, h2 |
| `1.30rem` | h1 page titles |

All headings, badges, and buttons: **font-weight 700**.

---

## Spacing

| Rule | Value |
|---|---|
| Shell max-width | 1100px, centered |
| Shell vertical padding | 2rem 1.5rem |
| Panel padding | 1.25rem 1.5rem |
| Margin between sections | 1.5rem |
| Definition list grid gap | 0.3rem × 1.5rem |
| Flex group gap (actions) | 0.5rem |
| Nav gap | 1.5rem |

---

## Corners

| Element | Radius |
|---|---|
| Panels | 4px |
| Buttons | 3px |
| Badges | 3px |
| Inputs | 3px |
| Flash messages | 3px |

---

## Buttons

| Class | Background | Text | Border |
|---|---|---|---|
| `.btn-primary` | `#8dff9a` | `#07100d` | `#8dff9a` |
| `.btn-ghost` | transparent | `#e8f6df` | `#1e3328` → hover `#88a393` |
| `.btn-danger` | transparent → hover `rgba(255,107,74,0.08)` | `#ff6b4a` | `rgba(255,107,74,0.35)` |

All buttons: `0.82rem`, `font-weight 700`, `letter-spacing 0.04em`, `opacity 0.85` on hover.

---

## Visual reference

A visual SVG swatch card is available at `docs/design-palette.svg`.  
Open it in a browser to see all colors rendered with their hex values.

[View SVG palette →](file:///home/bateau/git/Sea-Shell/yt2sp/docs/design-palette.svg)
