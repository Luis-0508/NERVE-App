import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";

const htmlPath = join(process.cwd(), "out", "index.html");
const nginxPath = join(process.cwd(), "deploy", "nginx.conf");

if (!existsSync(htmlPath)) {
  throw new Error("out/index.html is missing; run npm run build first.");
}

const html = readFileSync(htmlPath, "utf8");
const nginx = readFileSync(nginxPath, "utf8");
const csp = nginx.match(/add_header\s+Content-Security-Policy\s+"([^"]+)"\s+always;/)?.[1];

if (!html.includes('class="app-shell"')) {
  throw new Error("The exported root page does not contain the NERVE application shell.");
}
if (!csp) {
  throw new Error("Nginx does not declare a Content-Security-Policy header.");
}

const inlineScripts = [...html.matchAll(/<script(?![^>]*\bsrc=)[^>]*>([\s\S]*?)<\/script>/gi)]
  .filter((match) => match[1].trim().length > 0);
if (inlineScripts.length > 0 && !/script-src[^;]*'unsafe-inline'/.test(csp)) {
  throw new Error("The Next.js export contains inline bootstrap scripts that the Nginx CSP would block.");
}

const assetPaths = [...html.matchAll(/(?:src|href)="(\/_next\/static\/[^"?]+)(?:\?[^" ]*)?"/g)]
  .map((match) => match[1]);
for (const assetPath of new Set(assetPaths)) {
  const localPath = join(process.cwd(), "out", ...assetPath.split("/").filter(Boolean));
  if (!existsSync(localPath)) {
    throw new Error(`Exported asset is missing: ${assetPath}`);
  }
}

console.log(`Static runtime check passed (${inlineScripts.length} inline bootstrap scripts, ${new Set(assetPaths).size} assets).`);
