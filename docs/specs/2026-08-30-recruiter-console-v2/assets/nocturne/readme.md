# Nocturne design system (condensed from the Claude Design project)

Source: claude.ai/design project `b8f74b15-671d-45bc-8a33-642a9a468743`,
`_ds/nocturne-bace560e-548d-43c5-8a6d-0ae76799dba8/`. `styles.css` beside this
file is the token sheet + component layer, verbatim minus comments.

- Quiet, compact dark UI: ground `--color-bg` #161826, surface #232532, text
  #e9e9ed, single blurple accent #9184d9. Mono scheme — `accent-2` is a
  stand-in, treat as the same role.
- Every color/font/space/radius/shadow comes from a `var(--*)` token. Never
  hard-code a hex or px the tokens carry. Ramps 100–900 share one perceptual
  lightness scale: dark steps (700–900) for tints/hovers/borders, 500 base,
  light steps (100–300) for text on tints.
- Inter for headings (weight 500, never bolder) and body. Density 0.70×
  spacing scale; radius 8px.
- Buttons are outlined, never filled. Primary = accent outline.
- Freestanding rules fade to transparent over 48px at each end (`.hr`, table
  row rules). Box outlines and short accent marks stay solid.
- No pure black/white; no accent floods; no stacked heavy shadows.
- Interactive states: hover tint, pressed one ramp step past base,
  `:focus-visible` 2px accent ring.
- Icons: Phosphor, inline SVG on currentColor.
- Photographs through `.lighten` (mix-blend-mode).

Components: `.btn(-primary|-secondary|-ghost|-icon|-block)`, `.tag(-accent|
-accent-2|-neutral|-outline)`, `.field`+`label`, `.input`, `.radio`+`.dot`,
`.seg`+`.seg-opt`, `.card(-kicker|-title|-body|-meta)`, `.elev-sm/md/lg`,
`.nav`+`.nav-brand`, `.table`, `.dialog-backdrop`+`.dialog(-title|-body|
-actions)`, `.hr`, `.text-muted`.
