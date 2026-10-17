// Render ../png/*.png and ../proof.png from ../svg/*.svg. All PNGs are transparent except the proof.
import { readFileSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
const require = createRequire(import.meta.url); // resolves @resvg/resvg-js through NODE_PATH
const { Resvg } = require("@resvg/resvg-js");

const svgDir = new URL("../svg/", import.meta.url);
const pngDir = new URL("../png/", import.meta.url);
const read = (f) => readFileSync(new URL(f, svgDir), "utf8");
const png = (svg, width, file) =>
  writeFileSync(new URL(file, pngDir), new Resvg(svg, { fitTo: { mode: "width", value: width } }).render().asPng());

// GitHub App logo (small-size variant: it shows at 16-40 px): 512 px, transparent, the shield inset so a rounded crop never cuts it.
const inner = read("suiteward-mark-small.svg").replace(/^<svg[^>]*>/, "").replace(/<\/svg>\s*$/, "");
const app = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="-8 -8 116 116">${inner}</svg>`;
png(app, 512, "suiteward-github-app.png");
for (const w of [512, 256, 128]) png(read("suiteward-mark.svg"), w, `suiteward-mark-${w}.png`);
for (const w of [64, 48, 32, 16]) png(read("suiteward-mark-small.svg"), w, `suiteward-mark-small-${w}.png`);
for (const w of [1200, 2400]) {
  png(read("suiteward-logo.svg"), w, `suiteward-logo-${w}.png`);
  png(read("suiteward-logo-dark.svg"), w, `suiteward-logo-dark-${w}.png`);
}

// Proof: the App logo on GitHub's dark and light backgrounds at 88/40/20 px, and both logos.
const body = (f) => read(f).replace(/^<svg[^>]*>/, "").replace(/<\/svg>\s*$/, "").replace(/<title>.*?<\/title>/, "");
const at = (f, x, y, s, vb = "0 0 100 100") =>
  `<svg x="${x}" y="${y}" width="${s}" height="${s}" viewBox="${vb}">${body(f)}</svg>`;
let rows = "";
[["#0D1117", 0], ["#FFFFFF", 130]].forEach(([bg, y]) => {
  rows += `<rect x="0" y="${y}" width="420" height="130" fill="${bg}"/>`;
  rows += at("suiteward-mark.svg", 20, y + 21, 88) + at("suiteward-mark.svg", 130, y + 45, 40);
  rows += at("suiteward-mark-small.svg", 190, y + 45, 40) + at("suiteward-mark.svg", 250, y + 55, 20);
  rows += at("suiteward-mark-small.svg", 290, y + 55, 20) + at("suiteward-mark-small.svg", 330, y + 57, 16);
});
const logo = (f, y, bg) =>
  `<rect x="0" y="${y}" width="420" height="80" fill="${bg}"/><svg x="20" y="${y + 15}" width="${(439 * 50) / 100}" height="50" viewBox="0 0 439 100">${body(f)}</svg>`;
const proof = `<svg xmlns="http://www.w3.org/2000/svg" width="420" height="420" viewBox="0 0 420 420">${rows}${logo("suiteward-logo.svg", 260, "#F4F1EA")}${logo("suiteward-logo-dark.svg", 340, "#0D1117")}</svg>`;
writeFileSync(new URL("../proof.png", import.meta.url), new Resvg(proof, { fitTo: { mode: "width", value: 840 } }).render().asPng());
console.log("rendered");
