# SuiteWard brand

The mark is a shield holding a test list with one approved check: the test contract, guarded. [`proof.png`](proof.png) shows it on GitHub's dark and light backgrounds from 88 px down to 16 px, and both logos.

## Files

- `svg/`: the sources.
  - `suiteward-mark.svg`: the mark, at 48 px and above.
  - `suiteward-mark-small.svg`: the mark below 48 px, such as favicons and avatars. Its outline and bars are thicker so they survive downscaling.
  - `suiteward-logo.svg` and `suiteward-logo-dark.svg`: the horizontal logo for light and for dark backgrounds.
- `png/`: exports of the SVGs, all with a transparent background.
  - `suiteward-github-app.png`: the GitHub App logo, 512 × 512, the small-size mark with a margin.
  - `suiteward-mark-{512,256,128}.png` and `suiteward-mark-small-{64,48,32,16}.png`.
  - `suiteward-logo-{1200,2400}.png` and `suiteward-logo-dark-{1200,2400}.png`, named by width.
- `fonts/`: Inter Regular and SemiBold, the wordmark's fonts, with their license (`OFL.txt`).
- `source/`: the scripts that generate everything above.

## Colors

| Name | Hex | Role |
| --- | --- | --- |
| Ink | `#12202F` | The shield, and the wordmark on light backgrounds. |
| Green | `#3DD6A0` | The outline and the check: approval. |
| Paper | `#F4F1EA` | The test bars, and the wordmark on dark backgrounds. |

The green outline separates the shield from dark backgrounds, so keep it in every use. Do not recolor, rotate or stretch the mark. On the GitHub App settings page, set the badge background color to Ink.

## Wordmark

The wordmark is "Suite" in Inter Regular and "Ward" in Inter SemiBold, with Inter's kerning, converted to outlines, so the SVG and PNG files need no font installed. Inter is licensed under the SIL Open Font License 1.1, whose text is in `fonts/OFL.txt`.

## Regenerate

The shapes, colors and wordmark runs are defined in `source/gen.py`. After a change, regenerate the SVGs, then the PNGs and the proof:

```
python -m pip install fonttools==4.55.3
python assets/brand/source/gen.py
```

```
npm install --prefix <tool directory> @resvg/resvg-js@2.6.2
NODE_PATH=<tool directory>/node_modules node assets/brand/source/render.mjs
```

Install the tools outside the repository, in a directory of your choice.
