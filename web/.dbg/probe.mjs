import { chromium } from "playwright";
const B = "http://127.0.0.1:5199/";
const NAME = process.argv[2] || "2024年度产品设计评审会议纪要_最终修订版_v3_含附件与批注.pdf";
const browser = await chromium.launch();
for (const vp of [{ w: 1440, h: 900 }, { w: 375, h: 812 }]) {
  const page = await browser.newPage({ viewport: { width: vp.w, height: vp.h } });
  await page.goto(B);
  await page.waitForFunction(() => !!window.ElMessageBox);
  await page.evaluate((name) => {
    window.ElMessageBox.confirm(`删除 ${name}？`, "删除", {
      confirmButtonText: "确定", cancelButtonText: "取消", type: "warning",
    }).catch(() => {});
  }, NAME);
  await page.waitForSelector(".el-message-box");
  await page.waitForTimeout(400);
  const m = await page.evaluate(() => {
    const b = document.querySelector(".el-message-box");
    const r = b.getBoundingClientRect();
    const msg = document.querySelector(".el-message-box__message");
    const mr = msg.getBoundingClientRect();
    return { box: { w: Math.round(r.width), h: Math.round(r.height), x: Math.round(r.x), y: Math.round(r.y) },
             msg: { w: Math.round(mr.width), h: Math.round(mr.height) },
             vpH: innerHeight, scrollH: b.scrollHeight };
  });
  console.log(`${vp.w}x${vp.h}`, JSON.stringify(m));
  await page.screenshot({ path: `.dbg/shot-${vp.w}.png` });
  await page.close();
}
await browser.close();
