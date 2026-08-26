// One-off visual check for the responsive split login: screenshots + layout
// geometry at desktop and phone widths. Run: node .dbg/verify-auth.mjs
import { chromium } from "playwright";

const URL = "http://localhost:5173/login";
const viewports = [
  { name: "desktop-1280", width: 1280, height: 800 },
  { name: "phone-390", width: 390, height: 844 },
];

const browser = await chromium.launch();
for (const vp of viewports) {
  const page = await browser.newPage({ viewport: { width: vp.width, height: vp.height } });
  await page.goto(URL, { waitUntil: "domcontentloaded" });
  await page.waitForTimeout(1200);
  const geo = await page.evaluate(() => {
    const r = (sel) => {
      const el = document.querySelector(sel);
      if (!el) return null;
      const b = el.getBoundingClientRect();
      return { x: Math.round(b.x), y: Math.round(b.y), w: Math.round(b.width), h: Math.round(b.height) };
    };
    const cs = (sel, prop) => {
      const el = document.querySelector(sel);
      return el ? getComputedStyle(el)[prop] : null;
    };
    return {
      wrapDirection: cs(".auth-wrap", "flexDirection"),
      hero: r(".auth-hero"),
      pane: r(".auth-pane"),
      heroBg: (cs(".auth-hero", "backgroundImage") || "").slice(0, 60),
      nameColor: cs(".login-brand .app-name", "color"),
      icon: r(".login-brand .app-icon"),
      card: r(".auth-card"),
      langVisible: !!document.querySelector(".login-lang"),
    };
  });
  console.log(vp.name, JSON.stringify(geo));
  await page.screenshot({ path: `.dbg/auth-${vp.name}.png` });
  await page.close();
}
await browser.close();
