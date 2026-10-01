// 临时诊断探针的接收端：手机 APK 点「上报到电脑」时，把 JSON 落到本机磁盘。
//
// 用法：
//   node docs/probe-receiver.mjs            # 监听 0.0.0.0:5174
//   node docs/probe-receiver.mjs 5200       # 自定义端口
//
// 落盘位置：本仓库的 .probe-logs/ 下（已在 .gitignore 之外，注意别误提交）。
//
// 排查完成后连同这个脚本一起删除。

import fs from "node:fs";
import http from "node:http";
import path from "node:path";
import { fileURLToPath } from "node:url";

const PORT = Number(process.argv[2] || 5174);
const here = path.dirname(fileURLToPath(import.meta.url));
const OUT_DIR = path.resolve(here, "..", ".probe-logs");

fs.mkdirSync(OUT_DIR, { recursive: true });

function stamp() {
  const now = new Date();
  const pad = (value) => String(value).padStart(2, "0");
  return [
    now.getFullYear(),
    pad(now.getMonth() + 1),
    pad(now.getDate()),
    "-",
    pad(now.getHours()),
    pad(now.getMinutes()),
    pad(now.getSeconds()),
  ].join("");
}

const server = http.createServer((req, res) => {
  // APK 的 WebView 从别的源发请求，必须放行 CORS，否则 fetch 会被浏览器拦下。
  res.setHeader("Access-Control-Allow-Origin", "*");
  res.setHeader("Access-Control-Allow-Methods", "POST, OPTIONS");
  res.setHeader("Access-Control-Allow-Headers", "Content-Type");

  if (req.method === "OPTIONS") {
    res.writeHead(204);
    res.end();
    return;
  }

  if (req.method !== "POST") {
    res.writeHead(200, { "Content-Type": "text/plain; charset=utf-8" });
    res.end("mindfs probe receiver is running");
    return;
  }

  const chunks = [];
  req.on("data", (chunk) => chunks.push(chunk));
  req.on("end", () => {
    const body = Buffer.concat(chunks).toString("utf8");
    let parsed;
    try {
      parsed = JSON.parse(body);
    } catch (error) {
      console.error(`[probe] 收到无法解析的请求：${error.message}`);
      res.writeHead(400);
      res.end("invalid json");
      return;
    }

    const file = path.join(OUT_DIR, `probe-${stamp()}.json`);
    try {
      fs.writeFileSync(file, JSON.stringify(parsed, null, 2), "utf8");
    } catch (error) {
      console.error(`[probe] 写入失败：${error.message}`);
      res.writeHead(500);
      res.end(`write failed: ${error.message}`);
      return;
    }

    const counts = parsed?.counts || {};
    console.log(
      `[probe] 已保存 ${file} · 会话 ${counts.keys ?? "?"} · 异常 ${
        counts.suspicious ?? "?"
      }`,
    );

    res.writeHead(200, { "Content-Type": "text/plain; charset=utf-8" });
    res.end(`${path.basename(file)} · 异常 ${counts.suspicious ?? "?"}`);
  });
});

server.listen(PORT, "0.0.0.0", () => {
  console.log(`[probe] 监听 0.0.0.0:${PORT}，落盘到 ${OUT_DIR}`);
  console.log(`[probe] 手机端上报地址应填：http://<本机局域网IP>:${PORT}/__probe__`);
});
