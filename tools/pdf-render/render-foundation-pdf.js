/**
 * Render docs/FOUNDATION-SOVEREIGN-VS-COSMOS.md → PDF with drawn Mermaid diagrams.
 *
 * Usage (from repo root or this dir):
 *   node tools/pdf-render/render-foundation-pdf.js
 */
const fs = require("fs");
const path = require("path");
const { pathToFileURL } = require("url");
const { marked } = require("marked");
const puppeteer = require("puppeteer-core");

const repoRoot = path.resolve(__dirname, "../..");
const mdPath = path.join(repoRoot, "docs", "FOUNDATION-SOVEREIGN-VS-COSMOS.md");
const htmlPath = path.join(repoRoot, "docs", ".foundation-tmp.html");
const pdfPath = path.join(repoRoot, "docs", "FOUNDATION-SOVEREIGN-VS-COSMOS.pdf");
const chrome =
  process.env.PUPPETEER_EXECUTABLE_PATH ||
  "C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe";

function escapeHtml(s) {
  return s
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

const renderer = new marked.Renderer();
const defaultCode = renderer.code.bind(renderer);
renderer.code = function code(codeText, infostring, escaped) {
  const lang = (infostring || "").trim().split(/\s+/)[0];
  if (lang === "mermaid") {
    return `<div class="mermaid">${escapeHtml(codeText)}</div>\n`;
  }
  return defaultCode(codeText, infostring, escaped);
};

marked.setOptions({ renderer, gfm: true, breaks: false });

const md = fs.readFileSync(mdPath, "utf8");
const body = marked.parse(md);

const html = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <title>Foundation: Sovereign vs Cosmos</title>
  <style>
    body {
      font-family: "Segoe UI", system-ui, -apple-system, sans-serif;
      font-size: 10.5pt;
      line-height: 1.45;
      color: #1a1a1a;
      max-width: 900px;
      margin: 0 auto;
      padding: 8px 12px 24px;
    }
    h1 { font-size: 1.7rem; margin-top: 0; border-bottom: 2px solid #1a1a1a; padding-bottom: 0.35rem; }
    h2 { font-size: 1.25rem; margin-top: 1.6rem; page-break-after: avoid; }
    h3 { font-size: 1.05rem; margin-top: 1.2rem; page-break-after: avoid; }
    p, li { orphans: 3; widows: 3; }
    table { border-collapse: collapse; width: 100%; margin: 0.9em 0; font-size: 9.2pt; page-break-inside: avoid; }
    th, td { border: 1px solid #bbb; padding: 5px 7px; vertical-align: top; text-align: left; }
    th { background: #efefef; font-weight: 600; }
    tr:nth-child(even) td { background: #fafafa; }
    code { font-family: Consolas, "Cascadia Mono", ui-monospace, monospace; font-size: 0.88em; background: #f3f3f3; padding: 0.1em 0.3em; border-radius: 3px; }
    pre { background: #f5f5f5; border: 1px solid #e0e0e0; padding: 10px 12px; overflow-x: auto; font-size: 8.5pt; border-radius: 4px; }
    pre code { background: none; padding: 0; }
    blockquote { margin: 0.8em 0; padding: 0.4em 0.9em; border-left: 3px solid #666; color: #333; background: #f9f9f9; }
    hr { border: none; border-top: 1px solid #ccc; margin: 1.5rem 0; }
    .mermaid { margin: 1.1em 0; text-align: center; page-break-inside: avoid; background: #fcfcfc; border: 1px solid #e8e8e8; border-radius: 6px; padding: 12px 8px; }
    a { color: #0b57d0; text-decoration: none; }
    strong { font-weight: 600; }
  </style>
</head>
<body>
${body}
<script type="module">
  import mermaid from "https://cdn.jsdelivr.net/npm/mermaid@10.9.1/dist/mermaid.esm.min.mjs";
  mermaid.initialize({
    startOnLoad: false,
    theme: "neutral",
    securityLevel: "loose",
    flowchart: { htmlLabels: true, curve: "basis" },
    gantt: { fontSize: 11 },
  });
  await mermaid.run({ querySelector: ".mermaid" });
  document.body.dataset.ready = "true";
</script>
</body>
</html>
`;

fs.writeFileSync(htmlPath, html, "utf8");

(async () => {
  const browser = await puppeteer.launch({
    executablePath: chrome,
    headless: true,
    args: ["--no-sandbox", "--disable-gpu"],
  });
  try {
    const page = await browser.newPage();
    await page.goto(pathToFileURL(htmlPath).href, {
      waitUntil: "networkidle0",
      timeout: 120000,
    });
    await page.waitForFunction(() => document.body.dataset.ready === "true", {
      timeout: 90000,
    });
    await new Promise((r) => setTimeout(r, 1000));
    await page.pdf({
      path: pdfPath,
      format: "A4",
      printBackground: true,
      margin: { top: "14mm", bottom: "14mm", left: "12mm", right: "12mm" },
    });
    console.log("Wrote", pdfPath);
  } finally {
    await browser.close();
    try {
      fs.unlinkSync(htmlPath);
    } catch (_) {}
  }
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
