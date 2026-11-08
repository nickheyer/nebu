# Style

The look of nebu, with the palette and type it is built from.

## Identity

The reference objects are the Nebuchadnezzar and its operator deck: matte steel, cables, worn metal, a few cold light sources, and CRT screens showing the machine's own output in phosphor green. Nothing on that deck decorates. Every light means something.

The UI follows that in four decisions.

- **Surfaces are steel.** Cool neutral greys with a trace of blue, the same hue in both themes. The icon's charcoal and off white are the two anchors.
- **One light source.** The cyan of the hover pads is the only accent. It marks what is interactive or active and nothing else.
- **Status is an instrument lamp.** Green, amber and red are muted to lamp intensity and appear as dots and pills, never as fills on large areas.
- **Machine output lives on a screen.** Logs, bodies, commands and editors render on the dark screen surface with phosphor text in both themes. It is the one place the deck's CRTs show through.

No scanlines, no code rain, no glow, no gradients, no illustration.

## Palette

Light

| Role | Hex | OKLCH |
| --- | --- | --- |
| background | `#eceef3` | 94.9% 0.007 269 |
| surface | `#e0e2e7` | derived, background mixed with 5% black |
| text | `#1f2229` | 25.2% 0.014 267 |
| text muted | `#5b606c` | 48.9% 0.020 268 |
| border | `#cfd3db` | 86.6% 0.012 265 |
| primary | `#0b6f78` | 49.5% 0.082 205 |
| success | `#27703a` | 48.6% 0.111 149 |
| warning | `#8f5412` | 50.4% 0.108 62 |
| error | `#b3261e` | 50.1% 0.178 29 |
| steel | `#4f6378` | 49.2% 0.042 250 |

Dark

| Role | Hex | OKLCH |
| --- | --- | --- |
| background | `#15171c` | 20.5% 0.010 268 |
| surface | `#212327` | derived, background mixed with 5% white |
| text | `#eceef3` | 94.9% 0.007 269 |
| text muted | `#a8adb8` | 74.7% 0.017 266 |
| border | `#2c3038` | 30.9% 0.015 264 |
| primary | `#7fd9df` | 83.1% 0.087 201 |
| success | `#8ed48a` | 80.5% 0.124 143 |
| warning | `#e6b56f` | 80.3% 0.105 75 |
| error | `#f28b7d` | 74.4% 0.128 29 |
| steel | `#9fb0c4` | 75.1% 0.035 253 |

Screen, both themes

| Role | Hex | OKLCH |
| --- | --- | --- |
| screen background | `#0f1114` | 17.7% 0.007 258 |
| screen border | `#23272e` | 27.2% 0.014 262 |
| phosphor | `#9fd39a` | 81.6% 0.095 142 |
| phosphor muted | `#6f8f6c` | 61.6% 0.063 143 |

## Roles

| Use | Colour |
| --- | --- |
| links, active tab, primary button, selected row, progress | primary |
| ready, succeeded, connected | success |
| starting, pending, swapping, warnings | warning |
| failed, errors, destructive confirm | error |
| draining, stopping | primary |
| idle, stopped, disabled, secondary text | text muted |
| secondary accent, charts needing a second series | steel |
| log lines, request bodies, YAML, commands | phosphor on screen |

A primary button is a primary fill with background coloured text. Every other button is flat or raised steel with text coloured text. No button or card carries an outline. The one action of a row is a text button, raised buttons stand outside rows. Status never fills a card or a row, it colours a dot, a pill, or a bar.

## Disabled

A disabled control keeps its shape and loses its depth: the surface fill, muted text, no shadow, whatever it was when enabled. Text muted on surface measures 4.9 light and 7.0 dark.

## Contrast

Every text pair is measured, WCAG ratio against its background.

| Pair | Light | Dark |
| --- | --- | --- |
| text | 13.7 | 15.5 |
| text muted | 5.4 | 8.0 |
| primary | 5.1 | 11.0 |
| success | 5.2 | 10.2 |
| warning | 5.3 | 9.6 |
| error | 5.6 | 7.5 |
| steel | 5.3 | 8.1 |
| background on primary fill | 5.1 | 11.0 |
| phosphor on screen | 11.0 | 11.0 |
| phosphor muted on screen | 5.2 | 5.2 |

## Type

| Use | Family | Weight | Size |
| --- | --- | --- | --- |
| UI | IBM Plex Sans | 400, 500 labels, 600 headings | 15px base, 13px secondary, 12px pills |
| page title, a record's name | IBM Plex Sans | 600 | 24px |
| section title | IBM Plex Sans | 600 | 17px |
| numbers | IBM Plex Sans, tabular figures | 400 | as the text around them |
| ids, addresses, paths, versions | IBM Plex Mono | 400 | 13px |
| screens | IBM Plex Mono | 400 | 13px, line height 1.5 |

Both families ship with the app from `@fontsource`, OFL licensed, weights 400, 500, 600 for Sans and 400, 500 for Mono. No other family. No italics except in markdown the model wrote. Sentence case everywhere. Units follow numbers with a thin space: 42 GB, 180 ms.

## Surfaces

The library is neumorphic. The page is flat background. Three depths are used.

| Depth | Where |
| --- | --- |
| flat | page, list rows, tabs, text buttons, rail items |
| raised, level 2 | cards, tiles, the open panel, dialogs, primary buttons |
| inset, level 2 | the active rail item, the chosen segment |
| inset, level 1 | inputs, the memory bar track, the screen |

Light source top left, the library default. Borders are hairlines from the border colour, used only where a shadow would be too heavy: row separators, table lines, the screen edge. Radius 8px on controls and rows, 12px on cards and panels, pills full. The icon's 22% corner is the mark alone.

## Icons

Phosphor Icons, regular weight, 20px in rows and buttons, 16px in pills. One family, never mixed with emoji in the UI.

## Motion

150 ms ease out for hover, focus and toggles. 200 ms for panels and sheets sliding in. No bounce, no scale, no fade longer than 200 ms. `prefers-reduced-motion` removes the slides.

## Logo

The mark is `static/favicon.svg`: charcoal rounded square, two off white brackets, one cyan stroke. The wordmark is nebu in IBM Plex Sans 600, lowercase, text colour. Mark and wordmark sit 8px apart at 24px mark height in the rail. The mark never changes colour and never sits on the screen surface.

## Tokens

Implemented in `web/nebu/src/theme.css`, loaded before the app styles. The library derives every hover, secondary and shadow colour from these.

```css
:root, [neo-theme-root] {
	--neo-font-family: 'IBM Plex Sans', system-ui, sans-serif;
	--neo-white-soft: #eceef3;
	--neo-grey: #15171c;
	--neo-grey-dark: #1f2229;
	--neo-blue: #0b6f78;
	--neo-blue-dark: #7fd9df;
	--neo-purple: #4f6378;
	--neo-purple-light: #9fb0c4;
	--neo-green: #27703a;
	--neo-green-light: #8ed48a;
	--neo-orange: #8f5412;
	--neo-orange-light: #e6b56f;
	--neo-red: #b3261e;
	--neo-red-light: #f28b7d;
	--nebu-font-mono: 'IBM Plex Mono', ui-monospace, monospace;
	--nebu-screen-background: #0f1114;
	--nebu-screen-border: #23272e;
	--nebu-screen-text: #9fd39a;
	--nebu-screen-text-muted: #6f8f6c;
	--nebu-disabled-fill: #e0e2e7;
	--nebu-disabled-text: #5b606c;
}
```

`--nebu-disabled-fill` is the surface and `--nebu-disabled-text` the muted text of the theme, `#212327` and `#a8adb8` in the dark one.

`--neo-white-soft` is the light background and the dark text. `--neo-grey` is the dark background. `--neo-grey-dark` is the light text. The light accents are the plain tokens, the dark accents the `-light` and `-dark` ones, which the library switches by theme.

## Never

- A second accent hue.
- Status colour as a background.
- Phosphor outside a screen.
- Pure black or pure white anywhere.
- Shadows deeper than level 2.
- A font the app does not ship (exception is rendered model card markdown)
