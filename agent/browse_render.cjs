// Run by ARTEX with operator-configured, absolute Playwright paths and a pinned proxy.
const { chromium } = require(process.env.ARTEX_BROWSE_PLAYWRIGHT);
let browser;
async function close() { if (browser) await browser.close(); }
process.once("SIGTERM", () => close().finally(() => process.exit(1)));
(async () => {
  const [proxy, url] = process.argv.slice(1);
  browser = await chromium.launch({
    executablePath: process.env.ARTEX_BROWSE_CHROMIUM || undefined,
    headless: true,
    proxy: {server: proxy, bypass: "<-loopback>"},
    args: ["--disable-quic", "--force-webrtc-ip-handling-policy=disable_non_proxied_udp"],
  });
  const page = await browser.newPage({ serviceWorkers: "block", acceptDownloads: false });
  const response = await page.goto(url, {waitUntil: "domcontentloaded", timeout: 25000});
  if (!response) throw new Error("navigation returned no HTTP response");
  const status = response.status();
  let body = "";
  if (status < 400) {
    body = await page.locator("body").evaluate(el => el.innerText.slice(0, 30000), undefined, {timeout:5000});
  }
  process.stdout.write(JSON.stringify({status,body}));
})().catch(err => {process.stderr.write(String(err)); process.exitCode=1;}).finally(close);
